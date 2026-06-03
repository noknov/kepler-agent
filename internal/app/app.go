package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/agent"
	"github.com/wati/oncall-agent/internal/config"
	"github.com/wati/oncall-agent/internal/conversation"
	"github.com/wati/oncall-agent/internal/delegation"
	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/memory"
	"github.com/wati/oncall-agent/internal/observability"
	"github.com/wati/oncall-agent/internal/prompts"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/session"
	"github.com/wati/oncall-agent/internal/slack"
	codeTools "github.com/wati/oncall-agent/internal/toolkit/tools/code"
	gcpTools "github.com/wati/oncall-agent/internal/toolkit/tools/gcp"
	gitTools "github.com/wati/oncall-agent/internal/toolkit/tools/git"
	githubTools "github.com/wati/oncall-agent/internal/toolkit/tools/github"
	notionTools "github.com/wati/oncall-agent/internal/toolkit/tools/notion"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
	"github.com/wati/oncall-agent/internal/toolkit/tools/slacktool"
	youtrackTools "github.com/wati/oncall-agent/internal/toolkit/tools/youtrack"
)

func Run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	server, err := NewServer(cfg)
	if err != nil {
		return err
	}
	go pullWorkspaceRepos(ctx, cfg.Security.WorkspaceRoots, 10*time.Minute)
	return server.ListenAndServe(ctx)
}

type Server struct {
	cfg     config.Config
	slack   *slack.Client
	access  safety.AccessPolicy
	conv    *conversation.Service
	prompt  safety.PromptPolicy
	metrics *observability.Recorder
	mux     *http.ServeMux
}

func NewServer(cfg config.Config) (*Server, error) {
	prompts.LoadFromEnv()
	store, err := session.NewFileStore(cfg.Sessions.DataDir)
	if err != nil {
		return nil, err
	}

	var llmClient llm.Client
	if cfg.LLM.Protocol == "anthropic" {
		llmClient = llm.NewAnthropicClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Timeout, cfg.LLM.AnthropicFlavor)
	} else {
		llmClient = llm.NewKimiClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Timeout)
	}
	llmClient = llm.WrapClient(llmClient, llm.CapabilitiesFor(cfg.LLM.Provider, cfg.LLM.Protocol))
	slackClient := slack.NewClient(cfg.Slack.BotToken, cfg.Slack.BotUserID)
	if cfg.Slack.BotUserID == "" {
		if botUserID, err := slackClient.AuthTest(context.Background()); err == nil {
			slackClient.SetBotUserID(botUserID)
			cfg.Slack.BotUserID = botUserID
			log.Printf("resolved Slack bot user id with auth.test")
		} else {
			log.Printf("warning: could not resolve Slack bot user id: %v", err)
		}
	}

	recorder := observability.NewRecorder()
	workspacePolicy := safety.WorkspacePolicy{Roots: cfg.Security.WorkspaceRoots}
	commandPolicy := safety.NewCommandPolicy()
	redactor := safety.Redactor{WorkspaceRoots: cfg.Security.WorkspaceRoots}
	promptPolicy := safety.PromptPolicy{WorkspaceRoots: cfg.Security.WorkspaceRoots}

	delegates := delegation.NewManager(llmClient, cfg.LLM.Model, cfg.LLM.Thinking)
	_ = delegates.LoadMarkdown(filepath.Join(prompts.Dir(), "rules"), filepath.Join(prompts.Dir(), "skills"))

	tools := registry.New()
	tools.Register(codeTools.SearchTool{Paths: workspacePolicy})
	tools.Register(codeTools.ReadFileTool{Paths: workspacePolicy})
	gitBase := gitTools.Base{Paths: workspacePolicy, Guard: commandPolicy, Timeout: cfg.Tools.CommandTimeout}
	tools.Register(gitTools.FetchRefTool{Base: gitBase})
	tools.Register(gitTools.SearchRefTool{Base: gitBase})
	tools.Register(gitTools.ReadFileRefTool{Base: gitBase})
	tools.Register(gitTools.StatusTool{Base: gitBase})
	tools.Register(gitTools.LogTool{Base: gitBase})
	tools.Register(gitTools.ShowTool{Base: gitBase})
	tools.Register(gcpTools.LogsTool{
		GCloudPath:       cfg.Tools.GCloudPath,
		DefaultProject:   cfg.Tools.GCPDefaultProject,
		DefaultNamespace: cfg.Tools.GCPDefaultNamespace,
		DefaultCluster:   cfg.Tools.GKEDefaultCluster,
		DefaultRegion:    cfg.Tools.GKEDefaultRegion,
		Guard:            commandPolicy,
		Timeout:          cfg.Tools.CommandTimeout,
	})
	notionClient := notionTools.Client{
		Token:         cfg.Tools.NotionToken,
		DatabaseID:    cfg.Tools.NotionDatabaseID,
		TitleProperty: cfg.Tools.NotionTitleProperty,
		Version:       cfg.Tools.NotionVersion,
	}
	tools.Register(notionTools.SearchTool{Client: notionClient})
	tools.Register(notionTools.CreatePageTool{Client: notionClient})
	youtrackClient := youtrackTools.Client{BaseURL: cfg.Tools.YouTrackURL, Token: cfg.Tools.YouTrackToken}
	tools.Register(youtrackTools.GetIssueTool{Client: youtrackClient})
	tools.Register(youtrackTools.SearchTool{Client: youtrackClient})
	githubClient := githubTools.Client{
		Token:      cfg.Tools.GitHubToken,
		APIBaseURL: cfg.Tools.GitHubAPIBaseURL,
		Owner:      cfg.Tools.GitHubDefaultOwner,
		Repo:       cfg.Tools.GitHubDefaultRepo,
	}
	tools.Register(githubTools.DispatchWorkflowTool{Client: githubClient})
	tools.Register(githubTools.WorkflowRunsTool{Client: githubClient})
	tools.Register(slacktool.AskUserTool{Slack: slackClient})
	tools.Register(delegation.Tool{Manager: delegates})

	mem := memory.Builder{
		MaxMessages:     cfg.Sessions.MaxMessages,
		MaxToolChars:    cfg.Sessions.MaxToolChars,
		MaxThreadChars:  cfg.Sessions.MaxThreadChars,
		MaxSummaryChars: cfg.Sessions.MaxSummaryChars,
	}
	runner := agent.Runner{
		LLM:       llmClient,
		Model:     cfg.LLM.Model,
		Thinking:  cfg.LLM.Thinking,
		MaxTokens: cfg.LLM.MaxTokens,
		Temp:      cfg.LLM.Temperature,
		Tools:     tools,
		Format:    mem,
		Sanitize:  redactor,
		Observer:  recorder,
		MaxSteps:  32,
	}
	conv := conversation.NewService(store, slackClient, runner, mem, promptPolicy, redactor, recorder)
	conv.Format = slack.MarkdownToMrkdwn

	s := &Server{
		cfg:     cfg,
		slack:   slackClient,
		access:  safety.NewAccessPolicy(cfg.Security.AllowedUsers, cfg.Security.AllowedChannels),
		conv:    conv,
		prompt:  promptPolicy,
		metrics: recorder,
		mux:     http.NewServeMux(),
	}
	s.routes(tools)
	return s, nil
}

func (s *Server) routes(tools *registry.Registry) {
	s.mux.Handle("/metrics", s.metrics)
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/slack/events", s.handleSlackEvents)
	log.Printf("oncall-agent configured, tools=%s", strings.Join(tools.Names(), ", "))
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	httpServer := &http.Server{
		Addr:    s.cfg.HTTP.Addr,
		Handler: s.mux,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("oncall-agent listening on %s", s.cfg.HTTP.Addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) handleSlackEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var envelope slack.EventEnvelope
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if envelope.Type == "url_verification" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": envelope.Challenge})
		return
	}

	body := string(bodyBytes)
	if err := slack.VerifySignature(
		s.cfg.Slack.SigningSecret,
		r.Header.Get("X-Slack-Request-Timestamp"),
		body,
		r.Header.Get("X-Slack-Signature"),
		time.Now(),
	); err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))

	if envelope.Type != "event_callback" {
		return
	}
	go s.handleEvent(context.Background(), envelope.EventID, envelope.Event)
}

func (s *Server) handleEvent(ctx context.Context, eventID string, ev slack.Event) {
	switch ev.Type {
	case "app_home_opened":
		s.handleAppHome(ctx, ev)
	case "app_mention":
		s.handleMention(ctx, eventID, ev)
	case "message":
		s.handleMessage(ctx, eventID, ev)
	case "file_shared":
		s.handleFileShared(ctx, eventID, ev)
	case "reaction_added":
		if ev.Item.Type == "message" {
			s.metrics.Reaction(ev.Reaction)
		}
	}
}

func (s *Server) handleMention(ctx context.Context, eventID string, ev slack.Event) {
	if !isChannelMention(ev) {
		return
	}
	if ev.User == "" || ev.User == s.cfg.Slack.BotUserID || ev.BotID != "" {
		return
	}
	threadTS := ev.ConversationThreadTS()
	if !s.access.AllowsChannel(ev.Channel) {
		s.metrics.Denied()
		_, _ = s.slack.PostMessage(ctx, ev.Channel, threadTS, "<@"+ev.User+"> Sorry, this channel is not allowed to use this bot.")
		return
	}
	text := s.prompt.CleanUserText(s.cfg.Slack.BotUserID, ev.Text)
	text, parts := s.attachSlackFiles(ctx, text, ev.Files)
	if text == "" {
		text = prompts.AppMessage("empty_mention", "(The user mentioned me but didn't say anything specific. Greet them briefly and ask what they need help with. Reply in the same language the user used, or English by default.)")
	}
	s.conv.HandleMention(ctx, conversation.Request{
		EventID:      eventID,
		UserID:       ev.User,
		Channel:      ev.Channel,
		ThreadTS:     threadTS,
		Text:         text,
		ContentParts: parts,
	})
}

func (s *Server) handleMessage(ctx context.Context, eventID string, ev slack.Event) {
	if isAppDM(ev) {
		s.handleDirectMessage(ctx, eventID, ev)
		return
	}
	s.handleChannelReply(ctx, eventID, ev)
}

func (s *Server) handleFileShared(ctx context.Context, eventID string, ev slack.Event) {
	userID := firstNonEmpty(ev.User, ev.UserID)
	channelID := firstNonEmpty(ev.Channel, ev.ChannelID)
	if userID == "" || userID == s.cfg.Slack.BotUserID || channelID == "" {
		return
	}
	// Channel file uploads are only handled when Slack also emits an app_mention
	// event for the uploaded message. Standalone file_shared events have no
	// mention text, so responding there would violate the no-mention contract.
	if !isDMChannel(channelID) {
		return
	}
	file := ev.File
	if file.ID == "" {
		file.ID = ev.FileID
	}
	if file.ID == "" {
		return
	}
	if !s.access.AllowsUser(userID) {
		s.metrics.Denied()
		_, _ = s.slack.PostMessage(ctx, channelID, ev.ConversationThreadTS(), "<@"+userID+"> Sorry, you don't have permission to use this bot.")
		return
	}
	text, parts := s.attachSlackFiles(ctx, "", []slack.File{file})
	if text == "" {
		text = prompts.AppMessage("empty_dm", "(The user sent an app DM with a file but no text. Briefly describe what you can do with the file and ask for any missing context.)")
	}
	s.conv.HandleMention(ctx, conversation.Request{
		EventID:      eventID,
		UserID:       userID,
		Channel:      channelID,
		ThreadTS:     ev.ConversationThreadTS(),
		Text:         text,
		ContentParts: parts,
	})
}

func (s *Server) handleDirectMessage(ctx context.Context, eventID string, ev slack.Event) {
	if !isUserMessageSubtype(ev.Subtype) || ev.BotID != "" || ev.User == "" || ev.User == s.cfg.Slack.BotUserID {
		return
	}
	if !s.access.AllowsUser(ev.User) {
		s.metrics.Denied()
		_, _ = s.slack.PostMessage(ctx, ev.Channel, ev.ConversationThreadTS(), "<@"+ev.User+"> Sorry, you don't have permission to use this bot.")
		return
	}
	if isThreadReply(ev) {
		text, parts := s.attachSlackFiles(ctx, strings.TrimSpace(ev.Text), ev.Files)
		if text != "" && s.conv.HandleReply(ctx, conversation.Request{
			EventID:      eventID,
			UserID:       ev.User,
			Channel:      ev.Channel,
			ThreadTS:     ev.ThreadTS,
			Text:         text,
			ContentParts: parts,
		}) {
			return
		}
	}
	text := strings.TrimSpace(ev.Text)
	text, parts := s.attachSlackFiles(ctx, text, ev.Files)
	if text == "" {
		text = prompts.AppMessage("empty_dm", "(The user sent an empty app DM. Greet them briefly and ask what they need help with. Reply in the same language the user used, or English by default.)")
	}
	s.conv.HandleMention(ctx, conversation.Request{
		EventID:      eventID,
		UserID:       ev.User,
		Channel:      ev.Channel,
		ThreadTS:     ev.ConversationThreadTS(),
		Text:         text,
		ContentParts: parts,
	})
}

func (s *Server) handleChannelReply(ctx context.Context, eventID string, ev slack.Event) {
	if !isThreadReply(ev) || !isUserMessageSubtype(ev.Subtype) || ev.BotID != "" || ev.User == "" || ev.User == s.cfg.Slack.BotUserID {
		return
	}
	if !s.access.AllowsChannel(ev.Channel) {
		s.metrics.Denied()
		return
	}
	text, parts := s.attachSlackFiles(ctx, strings.TrimSpace(ev.Text), ev.Files)
	if text == "" {
		return
	}
	_ = s.conv.HandleReply(ctx, conversation.Request{
		EventID:      eventID,
		UserID:       ev.User,
		Channel:      ev.Channel,
		ThreadTS:     ev.ThreadTS,
		Text:         text,
		ContentParts: parts,
	})
}

func isAppDM(ev slack.Event) bool {
	return ev.ChannelType == "im" || isDMChannel(ev.Channel)
}

func isChannelMention(ev slack.Event) bool {
	return ev.Type == "app_mention" && !isAppDM(ev)
}

func isDMChannel(channel string) bool {
	return strings.HasPrefix(channel, "D")
}

func isUserMessageSubtype(subtype string) bool {
	return subtype == "" || subtype == "file_share"
}

func isThreadReply(ev slack.Event) bool {
	return ev.ThreadTS != "" && ev.ThreadTS != ev.TS
}

func appendSlackFiles(text string, files []slack.File) string {
	filesText := slack.FormatFiles(files)
	if filesText == "" {
		return strings.TrimSpace(text)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return filesText
	}
	return text + "\n\n" + filesText
}

func (s *Server) attachSlackFiles(ctx context.Context, text string, files []slack.File) (string, []llm.ContentPart) {
	text = appendSlackFiles(text, files)
	if excerpt := s.slackPDFExcerpts(ctx, files); excerpt != "" {
		text = strings.TrimSpace(text)
		if text == "" {
			text = excerpt
		} else {
			text += "\n\n" + excerpt
		}
	}
	return text, s.slackImageParts(ctx, files)
}

func (s *Server) slackPDFExcerpts(ctx context.Context, files []slack.File) string {
	blocks := make([]string, 0, len(files))
	for _, file := range files {
		if !slack.IsPDFFile(file) {
			continue
		}
		if file.Size > maxSlackPDFBytes {
			log.Printf("skip slack pdf %s: size %d exceeds limit %d", file.ID, file.Size, maxSlackPDFBytes)
			blocks = append(blocks, slack.FormatPDFExcerpt(slack.FileDisplayName(file), "[PDF too large to extract; max "+formatBytes(maxSlackPDFBytes)+"]"))
			continue
		}
		data, err := s.slack.DownloadFile(ctx, file, maxSlackPDFBytes)
		if err != nil {
			log.Printf("skip slack pdf %s: download failed: %v", file.ID, err)
			blocks = append(blocks, slack.FormatPDFExcerpt(slack.FileDisplayName(file), "[Could not download PDF from Slack: "+err.Error()+"]"))
			continue
		}
		if !slack.IsPDFData(data) {
			log.Printf("skip slack pdf %s: downloaded content is not a PDF", file.ID)
			blocks = append(blocks, slack.FormatPDFExcerpt(slack.FileDisplayName(file), "[Downloaded file is not a valid PDF]"))
			continue
		}
		text, err := slack.ExtractPDFText(data, maxSlackPDFTextChars)
		if err != nil {
			log.Printf("skip slack pdf %s: extract failed: %v", file.ID, err)
			blocks = append(blocks, slack.FormatPDFExcerpt(slack.FileDisplayName(file), "[Could not extract text from PDF; it may be scanned/image-only. Ask the user to paste key details or send a screenshot.]"))
			continue
		}
		blocks = append(blocks, slack.FormatPDFExcerpt(slack.FileDisplayName(file), text))
	}
	return strings.Join(blocks, "\n\n")
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.0f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (s *Server) slackImageParts(ctx context.Context, files []slack.File) []llm.ContentPart {
	parts := make([]llm.ContentPart, 0, len(files))
	for _, file := range files {
		mime := normalizedImageMIME(file)
		if mime == "" {
			continue
		}
		if file.Size > maxSlackImageBytes {
			log.Printf("skip slack image %s: size %d exceeds limit %d", file.ID, file.Size, maxSlackImageBytes)
			continue
		}
		data, err := s.slack.DownloadFile(ctx, file, maxSlackImageBytes)
		if err != nil {
			log.Printf("skip slack image %s: %v", file.ID, err)
			continue
		}
		actualMIME := sniffImageMIME(data)
		if actualMIME == "" {
			log.Printf("skip slack image %s: downloaded content is not a supported image", file.ID)
			continue
		}
		if actualMIME != mime {
			log.Printf("slack image %s declared %s but detected %s", file.ID, mime, actualMIME)
		}
		dataURL := "data:" + actualMIME + ";base64," + base64.StdEncoding.EncodeToString(data)
		parts = append(parts, llm.ImageURLPart(dataURL))
	}
	return parts
}

const (
	maxSlackImageBytes    = 8 << 20
	maxSlackPDFBytes      = 16 << 20
	maxSlackPDFTextChars  = slack.DefaultMaxPDFExtractChars
)

func normalizedImageMIME(file slack.File) string {
	mime := strings.ToLower(strings.TrimSpace(file.Mimetype))
	switch mime {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return mime
	}
	switch strings.ToLower(strings.TrimSpace(file.Filetype)) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func sniffImageMIME(data []byte) string {
	if len(data) >= 8 &&
		data[0] == 0x89 &&
		data[1] == 'P' &&
		data[2] == 'N' &&
		data[3] == 'G' &&
		data[4] == '\r' &&
		data[5] == '\n' &&
		data[6] == 0x1a &&
		data[7] == '\n' {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	if len(data) >= 12 &&
		string(data[0:4]) == "RIFF" &&
		string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	if len(data) >= 6 && (string(data[0:6]) == "GIF87a" || string(data[0:6]) == "GIF89a") {
		return "image/gif"
	}
	return ""
}

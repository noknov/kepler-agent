package app

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/session"
	"github.com/wati/oncall-agent/internal/slack"
	codeTools "github.com/wati/oncall-agent/internal/toolkit/tools/code"
	gcpTools "github.com/wati/oncall-agent/internal/toolkit/tools/gcp"
	gitTools "github.com/wati/oncall-agent/internal/toolkit/tools/git"
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
	_ = delegates.LoadMarkdown(filepath.Join("config", "rules"), filepath.Join("config", "skills"))

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
		MaxSteps:  16,
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
	case "reaction_added":
		if ev.Item.Type == "message" {
			s.metrics.Reaction(ev.Reaction)
		}
	}
}

func (s *Server) handleMention(ctx context.Context, eventID string, ev slack.Event) {
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
	text = appendSlackFiles(text, ev.Files)
	if text == "" {
		text = "(The user mentioned me but didn't say anything specific. Greet them briefly and ask what they need help with. Reply in the same language the user used, or English by default.)"
	}
	s.conv.HandleMention(ctx, conversation.Request{
		EventID:  eventID,
		UserID:   ev.User,
		Channel:  ev.Channel,
		ThreadTS: threadTS,
		Text:     text,
	})
}

func (s *Server) handleMessage(ctx context.Context, eventID string, ev slack.Event) {
	if isAppDM(ev) {
		s.handleDirectMessage(ctx, eventID, ev)
		return
	}
	s.handlePendingReply(ctx, eventID, ev)
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
	text := strings.TrimSpace(ev.Text)
	text = appendSlackFiles(text, ev.Files)
	if text == "" {
		text = "(The user sent an empty app DM. Greet them briefly and ask what they need help with. Reply in the same language the user used, or English by default.)"
	}
	s.conv.HandleMention(ctx, conversation.Request{
		EventID:  eventID,
		UserID:   ev.User,
		Channel:  ev.Channel,
		ThreadTS: ev.ConversationThreadTS(),
		Text:     text,
	})
}

func (s *Server) handlePendingReply(ctx context.Context, eventID string, ev slack.Event) {
	if !isUserMessageSubtype(ev.Subtype) || ev.BotID != "" || ev.User == "" || ev.ThreadTS == "" {
		return
	}
	if !s.access.AllowsChannel(ev.Channel) {
		s.metrics.Denied()
		return
	}
	s.conv.HandleReply(ctx, conversation.Request{
		EventID:  eventID,
		UserID:   ev.User,
		Channel:  ev.Channel,
		ThreadTS: ev.ThreadTS,
		Text:     appendSlackFiles(strings.TrimSpace(ev.Text), ev.Files),
	})
}

func isAppDM(ev slack.Event) bool {
	return ev.ChannelType == "im" || strings.HasPrefix(ev.Channel, "D")
}

func isUserMessageSubtype(subtype string) bool {
	return subtype == "" || subtype == "file_share"
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

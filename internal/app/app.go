package app

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wati/oncall-agent/internal/config"
	"github.com/wati/oncall-agent/internal/conversation"
	"github.com/wati/oncall-agent/internal/health"
	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/observability"
	"github.com/wati/oncall-agent/internal/prompts"
	"github.com/wati/oncall-agent/internal/reminder"
	"github.com/wati/oncall-agent/internal/runs"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/session"
	"github.com/wati/oncall-agent/internal/slack"
	"github.com/wati/oncall-agent/internal/toolkit/gitcache"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
	"github.com/wati/oncall-agent/internal/web"
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
	if cfg.Security.WorkspaceAutoFetch {
		go pullWorkspaceRepos(ctx, cfg.Security.WorkspaceRoots, gitcache.DefaultFetchTTL)
	}
	if server.ragManager != nil && cfg.RAG.BackgroundIndex {
		defer server.ragManager.Close()
		log.Printf("rag/indexer: starting async background prewarm loop")
		go server.ragManager.StartIndexLoop(ctx)
	} else if server.ragManager != nil {
		defer server.ragManager.Close()
		log.Printf("rag/indexer: background prewarm disabled; on-demand indexing will be used")
	}
	if server.health != nil {
		go server.health.Start(ctx)
	}
	defer server.reminderStore.Close()
	go server.reminders.Start(ctx)
	return server.ListenAndServe(ctx)
}

type Server struct {
	cfg            config.Config
	slack          *slack.Client
	access         safety.AccessPolicy
	conv           *conversation.Service
	prompt         safety.PromptPolicy
	metrics        *observability.Recorder
	runStore       runs.Store
	ragManager     ragManagerCloser
	health         *health.Service
	reminders      reminder.Scheduler
	reminderStore  *reminder.PGStore
	mux            *http.ServeMux
	modelPrefs     sync.Map
	webSearchPrefs sync.Map
	tokenUsage     tokenUsageProvider
}

type ragManagerCloser interface {
	StartIndexLoop(ctx context.Context)
	Close()
}

func NewServer(cfg config.Config) (*Server, error) {
	prompts.LoadFromEnv()
	store, err := session.NewFileStore(cfg.Sessions.DataDir)
	if err != nil {
		return nil, err
	}
	runStore, err := runs.NewFileStore(cfg.Observing.RunsDir)
	if err != nil {
		return nil, err
	}
	reminderStore, err := reminder.NewPGStore(context.Background(), cfg.Reminders.PostgresDSN)
	if err != nil {
		return nil, err
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
	runtime := newAgentRuntime(cfg, slackClient, reminderStore, recorder)
	healthService := health.NewService(runtime.Tools, cfg.Security.WorkspaceRoots, recorder, runtime.RAGManager != nil)
	recorder.SetCostRates(runtime.CostRates)
	conv := conversation.NewService(store, slackClient, runtime.Runner, runtime.Memory, runtime.Prompt, runtime.Redactor, recorder)
	conv.Format = slack.MarkdownToMrkdwn
	conv.RunStore = runStore
	conv.RunProvider = cfg.LLM.Provider
	conv.RunModel = cfg.LLM.Model
	conv.CostRates = runtime.CostRates
	conv.HealthSummary = healthService.SummaryPrompt
	if cfg.Tools.TTSAuto && cfg.Tools.TTSAPIKey != "" {
		conv.AutoTTS = newAutoTTSFunc(cfg, slackClient)
		conv.TTSSummarizer = newTTSSummarizer(cfg, runtime)
	}

	var ragManager ragManagerCloser
	if runtime.RAGManager != nil {
		ragManager = runtime.RAGManager
	}

	s := &Server{
		cfg:           cfg,
		slack:         slackClient,
		access:        safety.NewAccessPolicy(cfg.Security.AllowedUsers, cfg.Security.AllowedChannels),
		conv:          conv,
		prompt:        runtime.Prompt,
		metrics:       recorder,
		runStore:      runStore,
		ragManager:    ragManager,
		health:        healthService,
		reminders:     reminder.Scheduler{Store: reminderStore, Messenger: slackClient},
		reminderStore: reminderStore,
		mux:           http.NewServeMux(),
		tokenUsage:    newTokenUsageProvider(cfg.LLM.Provider, cfg.LLM.TokenUsage),
	}
	conv.ModelOverride = func(userID string) string {
		return s.modelPreference(userID)
	}
	conv.WebSearchEnabled = func(userID string) bool {
		return s.webSearchPreference(userID)
	}
	if mm := cfg.LLM.MultimodalModels; len(mm) > 0 {
		mmSet := make(map[string]bool, len(mm))
		for _, m := range mm {
			mmSet[m] = true
		}
		conv.Multimodal = func(model string) bool {
			return mmSet[model]
		}
	}
	s.routes(cfg, store, runtime, runStore, recorder, healthService, runtime.Tools)
	return s, nil
}

func (s *Server) routes(cfg config.Config, store session.Store, runtime agentRuntime, runStore runs.Store, recorder *observability.Recorder, healthService *health.Service, tools *registry.Registry) {
	s.mux.Handle("/metrics", s.observabilityHandler(s.metrics))
	s.mux.HandleFunc("/health/dashboard", s.handleHealthDashboard)
	s.mux.HandleFunc("/health/tools", s.handleToolHealth)
	s.mux.HandleFunc("/health/tools/rag", s.handleRAGHealth)
	s.mux.HandleFunc("/runs", s.handleRuns)
	s.mux.HandleFunc("/runs/", s.handleRun)
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/slack/events", s.handleSlackEvents)
	s.mux.HandleFunc("/slack/interactions", s.handleSlackInteractions)

	webHub := web.NewHub()
	webMessenger := web.NewHubMessenger(webHub)
	webPrompt := safety.PromptPolicy{
		WorkspaceRoots:             cfg.Security.WorkspaceRoots,
		IncludeRepositoryInventory: cfg.Security.PromptIncludeRepoInventory,
	}
	webConv := conversation.NewService(store, webMessenger, runtime.Runner, runtime.Memory, webPrompt, runtime.Redactor, recorder)
	webConv.RunStore = runStore
	webConv.RunProvider = cfg.LLM.Provider
	webConv.RunModel = cfg.LLM.Model
	webConv.CostRates = runtime.CostRates
	webConv.HealthSummary = healthService.SummaryPrompt
	webConv.ModelOverride = func(userID string) string {
		return s.modelPreference(userID)
	}
	web.New(s.slack, webConv, webHub, cfg.Security.AllowedUsers, web.ModelSettings{
		DefaultModel: cfg.LLM.Model,
		Models:       cfg.LLM.AvailableModels,
		Get:          s.modelPreference,
		Set:          s.setModelPreference,
	}).RegisterRoutes(s.mux)

	log.Printf("oncall-agent configured, tools=%s", strings.Join(tools.Names(), ", "))
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	httpServer := &http.Server{
		Addr:              s.cfg.HTTP.Addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
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

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))

	if envelope.Type != "event_callback" {
		return
	}
	go s.handleEvent(context.Background(), envelope.EventID, envelope.Event)
}

func (s *Server) handleSlackInteractions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rawBody := string(bodyBytes)
	if err := slack.VerifySignature(
		s.cfg.Slack.SigningSecret,
		r.Header.Get("X-Slack-Request-Timestamp"),
		rawBody,
		r.Header.Get("X-Slack-Signature"),
		time.Now(),
	); err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	body := extractFormPayload(rawBody)
	if body == "" {
		http.Error(w, "missing payload", http.StatusBadRequest)
		return
	}

	var payload struct {
		Type string `json:"type"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Actions []struct {
			ActionID       string `json:"action_id"`
			SelectedOption struct {
				Value string `json:"value"`
			} `json:"selected_option"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)

	if payload.Type == "block_actions" {
		for _, action := range payload.Actions {
			switch action.ActionID {
			case "select_model":
				if action.SelectedOption.Value != "" {
					s.handleModelSelect(payload.User.ID, action.SelectedOption.Value)
				}
			case "toggle_web_search":
				s.handleWebSearchToggle(payload.User.ID)
			}
		}
	}
}

func (s *Server) handleModelSelect(userID, model string) {
	if !s.setModelPreference(userID, model) {
		return
	}
	go func() {
		if err := s.slack.PublishHome(context.Background(), userID, s.homeView(userID)); err != nil {
			log.Printf("publish home after model select failed: %v", err)
		}
	}()
}

func (s *Server) modelPreference(userID string) string {
	if v, ok := s.modelPrefs.Load(userID); ok {
		return v.(string)
	}
	return ""
}

func (s *Server) setModelPreference(userID, model string) bool {
	allowed := false
	for _, m := range s.cfg.LLM.AvailableModels {
		if m == model {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	if model == s.cfg.LLM.Model {
		s.modelPrefs.Delete(userID)
	} else {
		s.modelPrefs.Store(userID, model)
	}
	return true
}

func (s *Server) webSearchPreference(userID string) bool {
	v, ok := s.webSearchPrefs.Load(userID)
	if !ok {
		return true // default On
	}
	return v.(bool)
}

func (s *Server) handleWebSearchToggle(userID string) {
	s.webSearchPrefs.Store(userID, !s.webSearchPreference(userID))
	go func() {
		if err := s.slack.PublishHome(context.Background(), userID, s.homeView(userID)); err != nil {
			log.Printf("publish home after web search toggle failed: %v", err)
		}
	}()
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
			s.recordReactionFeedback(ctx, ev)
		}
	}
}

func (s *Server) recordReactionFeedback(ctx context.Context, ev slack.Event) {
	if s.runStore == nil || ev.Item.Channel == "" || ev.Item.TS == "" || ev.Reaction == "" {
		return
	}
	_, ok, err := s.runStore.AddFeedbackForMessage(ctx, ev.Item.Channel, ev.Item.TS, runs.Feedback{
		Source:    "slack_reaction",
		Value:     ev.Reaction,
		UserID:    ev.User,
		Channel:   ev.Item.Channel,
		MessageTS: ev.Item.TS,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		log.Printf("record reaction feedback failed channel=%s ts=%s reaction=%s err=%v", ev.Item.Channel, ev.Item.TS, ev.Reaction, err)
		return
	}
	if !ok {
		log.Printf("reaction feedback had no matching run channel=%s ts=%s reaction=%s", ev.Item.Channel, ev.Item.TS, ev.Reaction)
	}
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeObservability(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	runsList, err := s.runStore.List(r.Context(), limit)
	if err != nil {
		http.Error(w, "failed to list runs", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(runsList)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeObservability(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/runs/")
	run, ok, err := s.runStore.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "failed to read run", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(run)
}

func (s *Server) handleToolHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeObservability(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.health == nil {
		http.Error(w, "tool health monitor unavailable", http.StatusServiceUnavailable)
		return
	}
	snapshot := s.health.Snapshot()
	if strings.EqualFold(r.URL.Query().Get("refresh"), "true") {
		snapshot = s.health.Probe(r.Context())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}

func (s *Server) handleRAGHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeObservability(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.health == nil {
		http.Error(w, "tool health monitor unavailable", http.StatusServiceUnavailable)
		return
	}
	if strings.EqualFold(r.URL.Query().Get("refresh"), "true") {
		_ = s.health.Probe(r.Context())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.health.RAGSnapshot())
}

func (s *Server) observabilityHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorizeObservability(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorizeObservability(r *http.Request) bool {
	token := strings.TrimSpace(s.cfg.Observing.AdminToken)
	if token == "" {
		return s.cfg.Observing.AllowUnauthenticated && isLocalRequest(r)
	}
	got := strings.TrimSpace(r.Header.Get("X-Oncall-Agent-Admin-Token"))
	if got == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			got = strings.TrimSpace(auth[len("Bearer "):])
		}
	}
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func isLocalRequest(r *http.Request) bool {
	if r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) handleMention(ctx context.Context, eventID string, ev slack.Event) {
	if !isChannelMention(ev) {
		return
	}
	if ev.User == "" || ev.User == s.cfg.Slack.BotUserID || ev.BotID != "" {
		return
	}
	threadTS := ev.ConversationThreadTS()
	if !s.access.IsAllowed(ev.User, ev.Channel) {
		s.metrics.Denied()
		_, _ = s.slack.PostMessage(ctx, ev.Channel, threadTS, "<@"+ev.User+"> Sorry, you don't have permission to use this bot here.")
		return
	}
	text := s.prompt.CleanUserText(s.cfg.Slack.BotUserID, ev.Text)
	text, parts := s.attachSlackFiles(ctx, text, ev.Files)
	if text == "" {
		text = prompts.AppMessage("empty_mention", "")
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
	if ev.ConversationThreadTS() == "" {
		log.Printf("skip slack file_shared %s: no message timestamp; waiting for message.file_share event", file.ID)
		return
	}
	if !s.access.AllowsUser(userID) {
		s.metrics.Denied()
		_, _ = s.slack.PostMessage(ctx, channelID, ev.ConversationThreadTS(), "<@"+userID+"> Sorry, you don't have permission to use this bot.")
		return
	}
	text, parts := s.attachSlackFiles(ctx, "", []slack.File{file})
	if text == "" {
		text = prompts.AppMessage("empty_dm_with_file", "")
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
		text = prompts.AppMessage("empty_dm", "")
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
	// When a user @-mentions the bot in a thread, Slack fires both an
	// app_mention event (handled by handleMention) AND a message event here.
	// Skip the message event for @mentions to avoid double-processing: the
	// app_mention handler already starts a new run or enqueues the request.
	if s.cfg.Slack.BotUserID != "" && strings.Contains(ev.Text, "<@"+s.cfg.Slack.BotUserID+">") {
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
	if excerpt := s.slackTextExcerpts(ctx, files); excerpt != "" {
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

func (s *Server) slackTextExcerpts(ctx context.Context, files []slack.File) string {
	blocks := make([]string, 0, len(files))
	for _, file := range files {
		if !shouldAttemptSlackTextExcerpt(file) {
			continue
		}
		declaredText := slack.IsTextFile(file)
		if file.Size > maxSlackTextBytes {
			log.Printf("skip slack text %s: size %d exceeds limit %d", file.ID, file.Size, maxSlackTextBytes)
			if declaredText {
				blocks = append(blocks, slack.FormatTextExcerpt(slack.FileDisplayName(file), "[Text file too large to read; max "+formatBytes(maxSlackTextBytes)+"]"))
			}
			continue
		}
		data, err := s.slack.DownloadFile(ctx, file, maxSlackTextBytes)
		if err != nil {
			log.Printf("skip slack text %s: download failed: %v", file.ID, err)
			if declaredText {
				blocks = append(blocks, slack.FormatTextExcerpt(slack.FileDisplayName(file), "[Could not download text file from Slack: "+err.Error()+"]"))
			}
			continue
		}
		text, err := slack.ExtractTextFile(data, maxSlackTextChars)
		if err != nil {
			log.Printf("skip slack text %s: extract failed: %v", file.ID, err)
			if declaredText {
				blocks = append(blocks, slack.FormatTextExcerpt(slack.FileDisplayName(file), "[Could not read text file: "+err.Error()+"]"))
			}
			continue
		}
		blocks = append(blocks, slack.FormatTextExcerpt(slack.FileDisplayName(file), text))
	}
	return strings.Join(blocks, "\n\n")
}

func shouldAttemptSlackTextExcerpt(file slack.File) bool {
	return !slack.IsPDFFile(file) && normalizedImageMIME(file) == ""
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
	maxSlackImageBytes   = 8 << 20
	maxSlackPDFBytes     = 16 << 20
	maxSlackPDFTextChars = slack.DefaultMaxPDFExtractChars
	maxSlackTextBytes    = 16 << 20
	maxSlackTextChars    = slack.DefaultMaxTextExtractChars
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

func extractFormPayload(body string) string {
	values, err := url.ParseQuery(body)
	if err != nil {
		return ""
	}
	return values.Get("payload")
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

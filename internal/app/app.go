package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
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
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/slack-copilot-agent/internal/config"
	"github.com/noknov/slack-copilot-agent/internal/conversation"
	"github.com/noknov/slack-copilot-agent/internal/eventinbox"
	"github.com/noknov/slack-copilot-agent/internal/health"
	"github.com/noknov/slack-copilot-agent/internal/infra/envutil"
	"github.com/noknov/slack-copilot-agent/internal/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/internal/observability"
	"github.com/noknov/slack-copilot-agent/internal/prompts"
	"github.com/noknov/slack-copilot-agent/internal/reminder"
	"github.com/noknov/slack-copilot-agent/internal/runs"
	"github.com/noknov/slack-copilot-agent/internal/safety"
	"github.com/noknov/slack-copilot-agent/internal/session"
	"github.com/noknov/slack-copilot-agent/internal/slack"
	"github.com/noknov/slack-copilot-agent/internal/toolkit/gitcache"
	"github.com/noknov/slack-copilot-agent/internal/toolkit/tools/registry"
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
		server.Go(func(ctx context.Context) {
			pullWorkspaceRepos(ctx, cfg.Security.WorkspaceRoots, gitcache.DefaultFetchTTL)
		})
	}
	if server.health != nil {
		server.Go(func(ctx context.Context) {
			server.health.Start(ctx)
		})
	}
	defer server.Close()
	server.Go(func(ctx context.Context) {
		server.reminders.Start(ctx)
	})
	server.Go(func(ctx context.Context) {
		server.conv.StartControlSubscriber(ctx)
	})
	return server.ListenAndServe(ctx)
}

type Server struct {
	cfg                 config.Config
	slack               *slack.Client
	access              safety.AccessPolicy
	conv                *conversation.Service
	prompt              safety.PromptPolicy
	metrics             *observability.Recorder
	runStore            runs.Store
	health              *health.Service
	reminders           reminder.Scheduler
	reminderStore       *reminder.PGStore
	sessionStore        *session.PGStore
	runPGStore          *runs.PGStore
	eventInbox          *eventinbox.PGStore
	pgPool              *pgxpool.Pool
	redis               *redisclient.Client
	mux                 *http.ServeMux
	tokenUsage          tokenUsageProvider
	eventQueue          chan slackEventJob
	eventWorkers        int
	eventEnqueueTimeout time.Duration
	eventTimeout        time.Duration
	eventInboxLease     time.Duration
	runSemaphore        chan struct{}
	observabilityAPI    *ObservabilityAPI
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	eventMu             sync.Mutex
	eventCond           *sync.Cond
	activeEvents        int
	draining            atomic.Bool
}

func (s *Server) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.Wait(5 * time.Second)
	if s.redis != nil {
		_ = s.redis.Close()
	}
	if s.eventInbox != nil {
		s.eventInbox.Close()
	}
	if s.runPGStore != nil {
		s.runPGStore.Close()
	}
	if s.sessionStore != nil {
		s.sessionStore.Close()
	}
	if s.reminderStore != nil {
		s.reminderStore.Close()
	}
	if s.pgPool != nil {
		s.pgPool.Close()
	}
}

type slackEventJob struct {
	eventID string
	event   slack.Event
}

func NewServer(cfg config.Config) (*Server, error) {
	prompts.LoadFromEnv()
	serviceCtx, serviceCancel := context.WithCancel(context.Background())
	var cleanup []func()
	closeOnError := func() {
		serviceCancel()
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
	}

	pgCfg, err := pgxpool.ParseConfig(cfg.Storage.PostgresDSN)
	if err != nil {
		serviceCancel()
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	pgCfg.MaxConns = int32(envutil.Int("POSTGRES_MAX_CONNS", 20))
	pgCfg.MinConns = int32(envutil.Int("POSTGRES_MIN_CONNS", 2))
	pgPool, err := pgxpool.NewWithConfig(context.Background(), pgCfg)
	if err != nil {
		serviceCancel()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	cleanup = append(cleanup, pgPool.Close)

	rdb, err := redisclient.New(cfg.Storage.RedisURL)
	if err != nil {
		closeOnError()
		return nil, fmt.Errorf("redis: %w", err)
	}
	cleanup = append(cleanup, func() { _ = rdb.Close() })

	store, err := session.NewPGStoreWithPool(context.Background(), pgPool)
	if err != nil {
		closeOnError()
		return nil, err
	}
	runStore, err := runs.NewPGStoreWithPool(context.Background(), pgPool)
	if err != nil {
		closeOnError()
		return nil, err
	}
	reminderStore, err := reminder.NewPGStoreWithPool(context.Background(), pgPool)
	if err != nil {
		closeOnError()
		return nil, err
	}
	eventInbox, err := eventinbox.NewPGStoreWithPool(context.Background(), pgPool)
	if err != nil {
		closeOnError()
		return nil, err
	}

	gitcache.SetRedis(rdb)

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
	runtime := newAgentRuntime(cfg, slackClient, reminderStore, recorder, rdb)
	healthService := health.NewService(runtime.Tools, cfg.Security.WorkspaceRoots)
	healthService.Redis = rdb
	recorder.SetCostRates(runtime.CostRates)
	conv := conversation.NewService(store, slackClient, runtime.Runner, runtime.Memory, runtime.Prompt, runtime.Redactor, recorder)
	runSemaphore := make(chan struct{}, cfg.Tools.AgentMaxConcurrentRuns)
	conv.RunSemaphore = runSemaphore
	conv.MaxConcurrentRuns = cfg.Tools.AgentMaxConcurrentRuns
	conv.Redis = rdb
	conv.PodID = generatePodID()
	conv.FollowUpContext = serviceCtx
	conv.Format = slack.MarkdownToMrkdwn
	conv.RunStore = runStore
	conv.RunProvider = cfg.LLM.Provider
	conv.RunModel = cfg.LLM.Model
	multimodal := multimodalPredicate(cfg.LLM.MultimodalModels)
	conv.ModelRouter = conversation.ModelRouter{
		DefaultModel:            cfg.LLM.Model,
		MultimodalFallbackModel: cfg.LLM.MultimodalModel,
		SupportsMultimodal:      multimodal,
	}
	conv.CostRates = runtime.CostRates
	conv.HealthSummary = healthService.SummaryPrompt
	if cfg.Tools.TTSAuto && cfg.Tools.TTSAPIKey != "" {
		conv.AutoTTS = newAutoTTSFunc(cfg, slackClient)
		conv.TTSSummarizer = newTTSSummarizer(cfg, runtime)
	}

	s := &Server{
		cfg:                 cfg,
		slack:               slackClient,
		access:              safety.NewAccessPolicy(cfg.Security.AllowedUsers, cfg.Security.AllowedChannels),
		conv:                conv,
		prompt:              runtime.Prompt,
		metrics:             recorder,
		runStore:            runStore,
		sessionStore:        store,
		runPGStore:          runStore,
		health:              healthService,
		reminders:           reminder.Scheduler{Store: reminderStore, Messenger: slackClient, Redis: rdb},
		reminderStore:       reminderStore,
		eventInbox:          eventInbox,
		pgPool:              pgPool,
		redis:               rdb,
		mux:                 http.NewServeMux(),
		tokenUsage:          newTokenUsageProvider(cfg.LLM.Provider, cfg.LLM.TokenUsage),
		eventQueue:          make(chan slackEventJob, cfg.HTTP.EventQueueSize),
		eventWorkers:        cfg.HTTP.EventWorkers,
		eventEnqueueTimeout: cfg.HTTP.EventEnqueueTimeout,
		eventTimeout:        cfg.HTTP.EventTimeout,
		eventInboxLease:     cfg.HTTP.EventInboxLease,
		runSemaphore:        runSemaphore,
		ctx:                 serviceCtx,
		cancel:              serviceCancel,
	}
	s.eventCond = sync.NewCond(&s.eventMu)
	s.observabilityAPI = newObservabilityAPI(s)
	conv.WebSearchEnabled = func(userID string) bool {
		return s.webSearchPreference(userID)
	}
	conv.Multimodal = multimodal
	s.routes(cfg, runtime, recorder, healthService, runtime.Tools)
	cleanup = nil
	return s, nil
}

func (s *Server) routes(cfg config.Config, runtime agentRuntime, recorder *observability.Recorder, healthService *health.Service, tools *registry.Registry) {
	s.observabilityAPI.Register(s.mux)
	s.mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/readyz", s.handleReady)
	s.mux.HandleFunc("/drain", s.handleDrain)
	s.mux.HandleFunc("/slack/events", s.handleSlackEvents)
	s.mux.HandleFunc("/slack/interactions", s.handleSlackInteractions)

	log.Printf("slack-copilot-agent configured, tools=%s", strings.Join(tools.Names(), ", "))
}

func multimodalPredicate(models []string) func(string) bool {
	if len(models) == 0 {
		return nil
	}
	mmSet := make(map[string]bool, len(models))
	for _, m := range models {
		mmSet[m] = true
	}
	return func(model string) bool {
		return mmSet[model]
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	httpServer := &http.Server{
		Addr:              s.cfg.HTTP.Addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	s.startEventWorkers(s.ctx)
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		s.draining.Store(true)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.HTTP.ShutdownTimeout)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		if !s.waitEvents(s.cfg.HTTP.ShutdownTimeout) {
			log.Printf("shutdown: timed out waiting for in-flight Slack events")
		}
		if s.cancel != nil {
			s.cancel()
		}
	}()
	log.Printf("slack-copilot-agent listening on %s", s.cfg.HTTP.Addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		if s.cancel != nil {
			s.cancel()
		}
		return err
	}
	if ctx.Err() != nil {
		<-shutdownDone
	}
	return nil
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}
	if s.eventInbox == nil || s.sessionStore == nil || s.runPGStore == nil || s.reminderStore == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

func (s *Server) handleDrain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	if !isLocalRequest(r) {
		s.writeHTTPError(w, r, http.StatusForbidden, "forbidden", nil)
		return
	}
	s.draining.Store(true)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("draining"))
}

func (s *Server) handleSlackEvents(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		s.writeHTTPError(w, r, http.StatusServiceUnavailable, "server is draining", nil)
		return
	}
	if r.Method != http.MethodPost {
		s.writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		s.writeHTTPError(w, r, http.StatusBadRequest, "bad request", err)
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
		s.writeHTTPError(w, r, http.StatusUnauthorized, "invalid signature", err)
		return
	}

	var envelope slack.EventEnvelope
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		s.writeHTTPError(w, r, http.StatusBadRequest, "bad json", err)
		return
	}
	if envelope.Type == "url_verification" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": envelope.Challenge})
		return
	}

	if envelope.Type != "event_callback" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
	claimed, err := s.eventInbox.Claim(r.Context(), envelope.EventID, envelope.Event)
	if err != nil {
		s.writeHTTPError(w, r, http.StatusServiceUnavailable, "failed to persist event", err)
		return
	}
	if !claimed {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
	if !s.enqueueSlackEvent(r.Context(), envelope.EventID, envelope.Event) {
		s.writeHTTPError(w, r, http.StatusServiceUnavailable, "event queue is full; please retry", nil)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleSlackInteractions(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		s.writeHTTPError(w, r, http.StatusServiceUnavailable, "server is draining", nil)
		return
	}
	if r.Method != http.MethodPost {
		s.writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		s.writeHTTPError(w, r, http.StatusBadRequest, "bad request", err)
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
		s.writeHTTPError(w, r, http.StatusUnauthorized, "invalid signature", err)
		return
	}
	body := extractFormPayload(rawBody)
	if body == "" {
		s.writeHTTPError(w, r, http.StatusBadRequest, "missing payload", nil)
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
		s.writeHTTPError(w, r, http.StatusBadRequest, "bad json", err)
		return
	}

	w.WriteHeader(http.StatusOK)

	if payload.Type == "block_actions" {
		for _, action := range payload.Actions {
			switch action.ActionID {
			case "toggle_web_search":
				s.handleWebSearchToggle(payload.User.ID)
			}
		}
	}
}

func (s *Server) webSearchPreference(userID string) bool {
	if s.redis == nil {
		return true
	}
	return s.redis.GetBool(context.Background(), "websearch:"+userID, true)
}

func (s *Server) handleWebSearchToggle(userID string) {
	if s.redis == nil {
		return
	}
	_ = s.redis.SetBool(context.Background(), "websearch:"+userID, !s.webSearchPreference(userID))
	go func() {
		if err := s.slack.PublishHome(context.Background(), userID, s.homeView(userID)); err != nil {
			log.Printf("publish home after web search toggle failed: %v", err)
		}
	}()
}

func (s *Server) handleEvent(ctx context.Context, eventID string, ev slack.Event) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic while handling Slack event %s: %v", eventID, recovered)
		}
	}()
	switch ev.Type {
	case "app_home_opened":
		s.handleAppHome(ctx, ev)
		return nil
	case "app_mention":
		return s.handleMention(ctx, eventID, ev)
	case "message":
		return s.handleMessage(ctx, eventID, ev)
	case "file_shared":
		return s.handleFileShared(ctx, eventID, ev)
	case "reaction_added":
		if ev.Item.Type == "message" {
			s.metrics.Reaction(ev.Reaction)
			s.recordReactionFeedback(ctx, ev)
		}
	}
	return nil
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
		s.writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	if !s.authorizeObservability(r) {
		s.writeHTTPError(w, r, http.StatusForbidden, "forbidden", nil)
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
		s.writeHTTPError(w, r, http.StatusInternalServerError, "failed to list runs", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(runsList)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	if !s.authorizeObservability(r) {
		s.writeHTTPError(w, r, http.StatusForbidden, "forbidden", nil)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/runs/")
	run, ok, err := s.runStore.Get(r.Context(), id)
	if err != nil {
		s.writeHTTPError(w, r, http.StatusInternalServerError, "failed to read run", err)
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
		s.writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	if !s.authorizeObservability(r) {
		s.writeHTTPError(w, r, http.StatusForbidden, "forbidden", nil)
		return
	}
	if s.health == nil {
		s.writeHTTPError(w, r, http.StatusServiceUnavailable, "tool health monitor unavailable", nil)
		return
	}
	snapshot := s.health.Snapshot()
	if strings.EqualFold(r.URL.Query().Get("refresh"), "true") {
		snapshot = s.health.Probe(r.Context())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}

func (s *Server) observabilityHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorizeObservability(r) {
			s.writeHTTPError(w, r, http.StatusForbidden, "forbidden", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) writeHTTPError(w http.ResponseWriter, r *http.Request, status int, message string, err error) {
	if err != nil {
		log.Printf("http error method=%s path=%s status=%d remote=%s msg=%q err=%v", r.Method, r.URL.Path, status, r.RemoteAddr, message, err)
		if s.metrics != nil && status >= 500 {
			s.metrics.Error(fmt.Errorf("http %s %s: %s: %w", r.Method, r.URL.Path, message, err))
		}
	} else {
		log.Printf("http warning method=%s path=%s status=%d remote=%s msg=%q", r.Method, r.URL.Path, status, r.RemoteAddr, message)
		if s.metrics != nil && status >= 500 {
			s.metrics.Error(fmt.Errorf("http %s %s: %s", r.Method, r.URL.Path, message))
		}
	}
	http.Error(w, message, status)
}

func (s *Server) authorizeObservability(r *http.Request) bool {
	token := strings.TrimSpace(s.cfg.Observing.AdminToken)
	if token == "" {
		return s.cfg.Observing.AllowUnauthenticated && isLocalRequest(r)
	}
	got := strings.TrimSpace(r.Header.Get("X-Slack-Copilot-Agent-Admin-Token"))
	if got == "" {
		got = strings.TrimSpace(r.Header.Get("X-Oncall-Agent-Admin-Token"))
	}
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

func (s *Server) handleMention(ctx context.Context, eventID string, ev slack.Event) error {
	if !isChannelMention(ev) {
		return nil
	}
	if ev.User == "" || ev.User == s.cfg.Slack.BotUserID || ev.BotID != "" {
		return nil
	}
	threadTS := ev.ConversationThreadTS()
	if !s.access.IsAllowed(ev.User, ev.Channel) {
		s.metrics.Denied()
		_, _ = s.slack.PostMessage(ctx, ev.Channel, threadTS, "<@"+ev.User+"> Sorry, you don't have permission to use this bot here.")
		return nil
	}
	text := s.prompt.CleanUserText(s.cfg.Slack.BotUserID, ev.Text)
	text, parts := s.attachSlackFiles(ctx, text, ev.Files)
	if text == "" {
		text = prompts.AppMessage("empty_mention", "")
	}
	if !s.conv.HandleMention(ctx, conversation.Request{
		EventID:      eventID,
		UserID:       ev.User,
		Channel:      ev.Channel,
		ThreadTS:     threadTS,
		Text:         text,
		ContentParts: parts,
	}) {
		return fmt.Errorf("conversation did not accept app_mention event")
	}
	return nil
}

func (s *Server) handleMessage(ctx context.Context, eventID string, ev slack.Event) error {
	if isAppDM(ev) {
		return s.handleDirectMessage(ctx, eventID, ev)
	}
	return s.handleChannelReply(ctx, eventID, ev)
}

func (s *Server) handleFileShared(ctx context.Context, eventID string, ev slack.Event) error {
	userID := firstNonEmpty(ev.User, ev.UserID)
	channelID := firstNonEmpty(ev.Channel, ev.ChannelID)
	if userID == "" || userID == s.cfg.Slack.BotUserID || channelID == "" {
		return nil
	}
	// Channel file uploads are only handled when Slack also emits an app_mention
	// event for the uploaded message. Standalone file_shared events have no
	// mention text, so responding there would violate the no-mention contract.
	if !isDMChannel(channelID) {
		return nil
	}
	file := ev.File
	if file.ID == "" {
		file.ID = ev.FileID
	}
	if file.ID == "" {
		return nil
	}
	if ev.ConversationThreadTS() == "" {
		log.Printf("skip slack file_shared %s: no message timestamp; waiting for message.file_share event", file.ID)
		return nil
	}
	if !s.access.AllowsUser(userID) {
		s.metrics.Denied()
		_, _ = s.slack.PostMessage(ctx, channelID, ev.ConversationThreadTS(), "<@"+userID+"> Sorry, you don't have permission to use this bot.")
		return nil
	}
	text, parts := s.attachSlackFiles(ctx, "", []slack.File{file})
	if text == "" {
		text = prompts.AppMessage("empty_dm_with_file", "")
	}
	if !s.conv.HandleMention(ctx, conversation.Request{
		EventID:      eventID,
		UserID:       userID,
		Channel:      channelID,
		ThreadTS:     ev.ConversationThreadTS(),
		Text:         text,
		ContentParts: parts,
	}) {
		return fmt.Errorf("conversation did not accept file_shared event")
	}
	return nil
}

func (s *Server) handleDirectMessage(ctx context.Context, eventID string, ev slack.Event) error {
	if !isUserMessageSubtype(ev.Subtype) || ev.BotID != "" || ev.User == "" || ev.User == s.cfg.Slack.BotUserID {
		return nil
	}
	if !s.access.AllowsUser(ev.User) {
		s.metrics.Denied()
		_, _ = s.slack.PostMessage(ctx, ev.Channel, ev.ConversationThreadTS(), "<@"+ev.User+"> Sorry, you don't have permission to use this bot.")
		return nil
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
			return nil
		}
	}
	text := strings.TrimSpace(ev.Text)
	text, parts := s.attachSlackFiles(ctx, text, ev.Files)
	if text == "" {
		text = prompts.AppMessage("empty_dm", "")
	}
	if !s.conv.HandleMention(ctx, conversation.Request{
		EventID:      eventID,
		UserID:       ev.User,
		Channel:      ev.Channel,
		ThreadTS:     ev.ConversationThreadTS(),
		Text:         text,
		ContentParts: parts,
	}) {
		return fmt.Errorf("conversation did not accept direct message event")
	}
	return nil
}

func (s *Server) handleChannelReply(ctx context.Context, eventID string, ev slack.Event) error {
	if !isThreadReply(ev) || !isUserMessageSubtype(ev.Subtype) || ev.BotID != "" || ev.User == "" || ev.User == s.cfg.Slack.BotUserID {
		return nil
	}
	// When a user @-mentions the bot in a thread, Slack fires both an
	// app_mention event (handled by handleMention) AND a message event here.
	// Skip the message event for @mentions to avoid double-processing: the
	// app_mention handler already starts a new run or enqueues the request.
	if s.cfg.Slack.BotUserID != "" && strings.Contains(ev.Text, "<@"+s.cfg.Slack.BotUserID+">") {
		return nil
	}
	if !s.access.IsAllowed(ev.User, ev.Channel) {
		s.metrics.Denied()
		return nil
	}
	text, parts := s.attachSlackFiles(ctx, strings.TrimSpace(ev.Text), ev.Files)
	if text == "" {
		return nil
	}
	_ = s.conv.HandleReply(ctx, conversation.Request{
		EventID:      eventID,
		UserID:       ev.User,
		Channel:      ev.Channel,
		ThreadTS:     ev.ThreadTS,
		Text:         text,
		ContentParts: parts,
	})
	return nil
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

func generatePodID() string {
	if v := strings.TrimSpace(envutil.Env("HOSTNAME", "")); v != "" {
		return v
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "pod-" + hex.EncodeToString(b[:])
}

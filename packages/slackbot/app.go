package slackbot

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/slack-copilot-agent/packages/appsupport"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/conversation"
	"github.com/noknov/slack-copilot-agent/packages/eventinbox"
	"github.com/noknov/slack-copilot-agent/packages/health"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	"github.com/noknov/slack-copilot-agent/packages/platform"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
	"github.com/noknov/slack-copilot-agent/packages/reminder"
	"github.com/noknov/slack-copilot-agent/packages/runs"
	appruntime "github.com/noknov/slack-copilot-agent/packages/runtime"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/session"
	"github.com/noknov/slack-copilot-agent/packages/slack"
	"github.com/noknov/slack-copilot-agent/packages/slackevents"
	"github.com/noknov/slack-copilot-agent/packages/slackgateway"
	"github.com/noknov/slack-copilot-agent/packages/slackhandler"
	"github.com/noknov/slack-copilot-agent/packages/slackhome"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/gitcache"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

func Run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	server, err := NewServerWithOptions(cfg, AllInOneOptions())
	if err != nil {
		return err
	}
	defer server.Close()
	server.StartBackground()
	return server.ListenAndServe(ctx)
}

func (s *Server) StartBackground() {
	if s == nil || !s.opts.Background {
		return
	}
	if s.cfg.Security.WorkspaceAutoFetch {
		s.Go(func(ctx context.Context) {
			appsupport.PullWorkspaceRepos(ctx, s.cfg.Security.WorkspaceRoots, gitcache.DefaultFetchTTL)
		})
	}
	if s.health != nil {
		s.Go(func(ctx context.Context) {
			s.health.Start(ctx)
		})
	}
	s.Go(func(ctx context.Context) {
		s.reminders.Start(ctx)
	})
	s.Go(func(ctx context.Context) {
		s.conv.StartControlSubscriber(ctx)
	})
}

type Server struct {
	opts                Options
	cfg                 config.Config
	slack               *slack.Client
	access              safety.AccessPolicy
	conv                *conversation.Service
	handler             *slackhandler.Handler
	prompt              safety.PromptPolicy
	metrics             *observability.Recorder
	runStore            runs.Store
	health              *health.Service
	reminders           reminder.Scheduler
	reminderStore       *reminder.PGStore
	sessionStore        *session.PGStore
	runPGStore          *runs.PGStore
	eventInbox          *eventinbox.PGStore
	slackWorker         *slackevents.Worker
	slackGateway        *slackgateway.Gateway
	pgPool              *pgxpool.Pool
	redis               *redisclient.Client
	mux                 *http.ServeMux
	tokenUsage          tokenUsageProvider
	eventEnqueueTimeout time.Duration
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

func NewServer(cfg config.Config) (*Server, error) {
	return NewServerWithOptions(cfg, AllInOneOptions())
}

type conversationDeps struct {
	cfg            config.Config
	slackClient    *slack.Client
	runtime        appruntime.AgentRuntime
	recorder       *observability.Recorder
	sessionStore   session.Store
	runStore       runs.Store
	toolSpillStore registry.ToolSpillStore
	redis          *redisclient.Client
	runSemaphore   chan struct{}
	serviceCtx     context.Context
	multimodal     func(model string) bool
	healthSummary  func() string
}

func newConversationService(deps conversationDeps) *conversation.Service {
	conv := conversation.NewService(deps.sessionStore, deps.slackClient, deps.runtime.Runner, deps.runtime.Memory, deps.runtime.Prompt, deps.runtime.Redactor, deps.recorder)
	conv.RunSemaphore = deps.runSemaphore
	conv.MaxConcurrentRuns = deps.cfg.Tools.AgentMaxConcurrentRuns
	conv.Redis = deps.redis
	conv.PodID = appsupport.GeneratePodID()
	conv.FollowUpContext = deps.serviceCtx
	conv.Format = slack.MarkdownToMrkdwn
	conv.RunStore = deps.runStore
	conv.ToolSpillStore = deps.toolSpillStore
	conv.RunProvider = deps.cfg.LLM.Provider
	conv.RunModel = deps.cfg.LLM.Model
	conv.ModelRouter = conversation.ModelRouter{
		DefaultModel:            deps.cfg.LLM.Model,
		MultimodalFallbackModel: deps.cfg.LLM.MultimodalModel,
		SupportsMultimodal:      deps.multimodal,
	}
	conv.CostRates = deps.runtime.CostRates
	conv.HealthSummary = deps.healthSummary
	tts := deps.cfg.Integrations.TTS
	if tts.Auto && tts.APIKey != "" {
		conv.AutoTTS = appsupport.NewAutoTTSFunc(deps.cfg, deps.slackClient)
		conv.TTSSummarizer = appsupport.NewTTSSummarizer(deps.cfg, deps.runtime)
	}
	return conv
}

func NewServerWithOptions(cfg config.Config, opts Options) (*Server, error) {
	prompts.LoadFromEnv()
	serviceCtx, serviceCancel := context.WithCancel(context.Background())
	var cleanup []func()
	closeOnError := func() {
		serviceCancel()
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
	}

	stores, err := platform.NewStores(context.Background(), cfg.Storage)
	if err != nil {
		closeOnError()
		return nil, err
	}
	cleanup = append(cleanup, stores.Close)
	pgPool := stores.PGPool
	rdb := stores.Redis
	store := stores.Sessions
	runStore := stores.Runs
	reminderStore := stores.Reminders
	eventInbox := stores.Events

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
	runtime := appruntime.NewAgentRuntime(cfg, slackClient, reminderStore, recorder, rdb)
	healthService := health.NewService(runtime.Tools, cfg.Security.WorkspaceRoots)
	healthService.Redis = rdb
	recorder.SetCostRates(runtime.CostRates)
	runSemaphore := make(chan struct{}, cfg.Tools.AgentMaxConcurrentRuns)
	multimodal := multimodalPredicate(cfg.LLM.MultimodalModels)
	conv := newConversationService(conversationDeps{
		cfg:            cfg,
		slackClient:    slackClient,
		runtime:        runtime,
		recorder:       recorder,
		sessionStore:   store,
		runStore:       runStore,
		toolSpillStore: runStore,
		redis:          rdb,
		runSemaphore:   runSemaphore,
		serviceCtx:     serviceCtx,
		multimodal:     multimodal,
		healthSummary:  healthService.SummaryPrompt,
	})

	s := &Server{
		opts:          opts,
		cfg:           cfg,
		slack:         slackClient,
		access:        safety.NewAccessPolicy(cfg.Security.AllowedUsers, cfg.Security.AllowedChannels),
		conv:          conv,
		prompt:        runtime.Prompt,
		metrics:       recorder,
		runStore:      runStore,
		sessionStore:  store,
		runPGStore:    runStore,
		health:        healthService,
		reminders:     reminder.Scheduler{Store: reminderStore, Messenger: slackClient, Redis: rdb},
		reminderStore: reminderStore,
		eventInbox:    eventInbox,
		slackWorker: &slackevents.Worker{
			Inbox:        eventInbox,
			Redis:        rdb,
			Handler:      nil,
			Workers:      cfg.HTTP.EventWorkers,
			QueueSize:    cfg.HTTP.EventQueueSize,
			EventTimeout: cfg.HTTP.EventTimeout,
			InboxLease:   cfg.HTTP.EventInboxLease,
		},
		pgPool:              pgPool,
		redis:               rdb,
		mux:                 http.NewServeMux(),
		tokenUsage:          newTokenUsageProvider(cfg.LLM.Provider, cfg.LLM.TokenUsage),
		eventEnqueueTimeout: cfg.HTTP.EventEnqueueTimeout,
		runSemaphore:        runSemaphore,
		ctx:                 serviceCtx,
		cancel:              serviceCancel,
	}
	s.eventCond = sync.NewCond(&s.eventMu)
	s.handler = &slackhandler.Handler{
		Cfg:     cfg,
		Slack:   slackClient,
		Access:  s.access,
		Conv:    conv,
		Prompt:  runtime.Prompt,
		Metrics: recorder,
		Runs:    runStore,
		Home: slackhome.Controller{
			Cfg:    cfg,
			Access: s.access,
			Redis:  rdb,
			Slack:  slackClient,
		},
	}
	s.slackWorker.Handler = s.handler.Handle
	s.slackWorker.IsDraining = func() bool { return s.draining.Load() }
	s.slackWorker.BeginEvent = s.beginEvent
	s.slackWorker.EndEvent = s.endEvent
	s.slackWorker.StartGoroutine = s.Go
	s.slackGateway = &slackgateway.Gateway{
		SigningSecret: cfg.Slack.SigningSecret,
		Inbox:         eventInbox,
		IsDraining:    func() bool { return s.draining.Load() },
		OnWebSearch:   s.handler.ToggleWebSearch,
		WriteError:    s.writeHTTPError,
	}
	if opts.SlackWorker {
		s.slackGateway.Enqueue = s.enqueueSlackEvent
	} else {
		s.slackGateway.Publish = s.slackWorker.Publish
	}
	s.observabilityAPI = newObservabilityAPI(s)
	conv.WebSearchEnabled = func(userID string) bool {
		return s.handler.WebSearchPreference(userID)
	}
	conv.Multimodal = multimodal
	s.routes(cfg, runtime, recorder, healthService, runtime.Tools)
	cleanup = nil
	return s, nil
}

func (s *Server) routes(cfg config.Config, runtime appruntime.AgentRuntime, recorder *observability.Recorder, healthService *health.Service, tools *registry.Registry) {
	if s.opts.Observability {
		s.observabilityAPI.Register(s.mux)
	}
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
	if s.opts.SlackGateway {
		s.mux.HandleFunc("/slack/events", s.handleSlackEvents)
		s.mux.HandleFunc("/slack/interactions", s.handleSlackInteractions)
	}

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
	if s.opts.SlackWorker {
		s.startEventWorkers(s.ctx)
	}
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

func (s *Server) RunUntilDone(ctx context.Context) error {
	if s.opts.SlackWorker {
		s.startEventWorkers(s.ctx)
	}
	<-ctx.Done()
	s.draining.Store(true)
	if !s.waitEvents(s.cfg.HTTP.ShutdownTimeout) {
		log.Printf("shutdown: timed out waiting for in-flight Slack events")
	}
	if s.cancel != nil {
		s.cancel()
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
	if s.slackGateway == nil {
		s.writeHTTPError(w, r, http.StatusServiceUnavailable, "slack gateway unavailable", nil)
		return
	}
	s.slackGateway.HandleEvents(w, r)
}

func (s *Server) handleSlackInteractions(w http.ResponseWriter, r *http.Request) {
	if s.slackGateway == nil {
		s.writeHTTPError(w, r, http.StatusServiceUnavailable, "slack gateway unavailable", nil)
		return
	}
	s.slackGateway.HandleInteractions(w, r)
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
		got = strings.TrimSpace(r.Header.Get("X-Slack-Copilot-Admin-Token"))
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

func isAppDM(ev slack.Event) bool {
	return slackhandler.IsAppDM(ev)
}

func isChannelMention(ev slack.Event) bool {
	return slackhandler.IsChannelMention(ev)
}

func isDMChannel(channel string) bool {
	return slackhandler.IsDMChannel(channel)
}

func isUserMessageSubtype(subtype string) bool {
	return slackhandler.IsUserMessageSubtype(subtype)
}

func isThreadReply(ev slack.Event) bool {
	return slackhandler.IsThreadReply(ev)
}

func firstNonEmpty(values ...string) string {
	return slackhandler.FirstNonEmpty(values...)
}

func generatePodID() string {
	return appsupport.GeneratePodID()
}

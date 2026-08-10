package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/hosted"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/legacy"
	agentruntimev2 "github.com/noknov/slack-copilot-agent/packages/agentv2/runtime"
	"github.com/noknov/slack-copilot-agent/packages/appsupport"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/conversation"
	"github.com/noknov/slack-copilot-agent/packages/conversationv2"
	"github.com/noknov/slack-copilot-agent/packages/health"
	"github.com/noknov/slack-copilot-agent/packages/infra/httpguard"
	sharedlogging "github.com/noknov/slack-copilot-agent/packages/infra/logging"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	"github.com/noknov/slack-copilot-agent/packages/platform"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
	"github.com/noknov/slack-copilot-agent/packages/reminder"
	appruntime "github.com/noknov/slack-copilot-agent/packages/runtime"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/slack"
	"github.com/noknov/slack-copilot-agent/packages/slackevents"
	"github.com/noknov/slack-copilot-agent/packages/slackhandler"
	"github.com/noknov/slack-copilot-agent/packages/slackhome"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/gitcache"
)

type controlledConversation interface {
	slackhandler.Conversation
	StartControlSubscriber(context.Context)
}

func runtimeVersion() string {
	version := strings.ToLower(strings.TrimSpace(os.Getenv("AGENT_RUNTIME_VERSION")))
	if version == "v1" {
		return "v1"
	}
	return "v2"
}

type Service struct {
	cfg         config.Config
	stores      *platform.Stores
	slack       *slack.Client
	runtime     appruntime.AgentRuntime
	metrics     *observability.Recorder
	health      *health.Service
	reminders   reminder.Scheduler
	conv        controlledConversation
	handler     *slackhandler.Handler
	slackWorker *slackevents.Worker

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	eventMu      sync.Mutex
	eventCond    *sync.Cond
	activeEvents int
	draining     atomic.Bool
	serveErr     chan error
}

func Run(ctx context.Context) error {
	cfg, err := config.LoadFor(config.ProfileSlackWorker)
	if err != nil {
		return err
	}
	sharedlogging.Configure(cfg.Observing.LogLevel)
	service, err := New(ctx, cfg)
	if err != nil {
		return err
	}
	defer service.Close()
	service.StartBackground()
	return service.RunUntilDone(ctx)
}

func New(ctx context.Context, cfg config.Config) (*Service, error) {
	prompts.LoadFromEnv()
	serviceCtx, serviceCancel := context.WithCancel(context.Background())
	stores, err := platform.NewStores(ctx, cfg.Storage)
	if err != nil {
		serviceCancel()
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			serviceCancel()
			stores.Close()
		}
	}()

	gitcache.SetRedis(stores.Redis)

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
	rt := appruntime.NewAgentRuntime(cfg, slackClient, stores.Reminders, recorder, stores.Redis, stores.UserPrefs)
	if rt.Core != nil {
		rt.Core.Events = stores.Protocol
	}
	recorder.SetCostRates(rt.CostRates)

	healthService := health.NewService(rt.Tools, cfg.Security.WorkspaceRoots)
	healthService.Redis = stores.Redis

	convV1 := conversation.NewService(stores.Sessions, slackClient, rt.Runner, rt.Memory, rt.Prompt, rt.Redactor, recorder)
	conv := controlledConversation(convV1)
	convV1.Core = rt.Core
	runSemaphore := make(chan struct{}, cfg.Tools.AgentMaxConcurrentRuns)
	convV1.RunSemaphore = runSemaphore
	convV1.MaxConcurrentRuns = cfg.Tools.AgentMaxConcurrentRuns
	convV1.Redis = stores.Redis
	podID := appsupport.GeneratePodID()
	convV1.PodID = podID
	convV1.FollowUpContext = serviceCtx
	convV1.Format = slack.MarkdownToMrkdwn
	convV1.RunStore = stores.Runs
	convV1.ToolSpillStore = stores.Runs
	convV1.UserPrefs = stores.UserPrefs
	convV1.RunProvider = cfg.LLM.Provider
	convV1.RunModel = cfg.LLM.Model
	multimodal := multimodalPredicate(cfg.LLM.MultimodalModels)
	convV1.ModelRouter = conversation.ModelRouter{
		DefaultModel:            cfg.LLM.Model,
		MultimodalFallbackModel: cfg.LLM.MultimodalModel,
		SupportsMultimodal:      multimodal,
	}
	convV1.CostRates = rt.CostRates
	convV1.HealthSummary = healthService.SummaryPrompt
	if cfg.Integrations.TTS.Auto && cfg.Integrations.TTS.APIKey != "" {
		convV1.AutoTTS = appsupport.NewAutoTTSFunc(cfg, slackClient)
		convV1.TTSSummarizer = appsupport.NewTTSSummarizer(cfg, rt)
	}

	if runtimeVersion() == "v2" {
		catalog, catalogErr := legacy.Catalog(rt.Tools.Clone())
		if catalogErr != nil {
			return nil, fmt.Errorf("build v2 tool catalog: %w", catalogErr)
		}
		v2conv := conversationv2.New(hosted.Agent{}, slackClient, rt.Prompt, rt.Redactor, stores.UserPrefs)
		runner, runnerErr := agentruntimev2.New(agentruntimev2.Config{Model: cfg.LLM.Model, ReasoningEffort: cfg.LLM.Thinking, MaxOutputTokens: cfg.LLM.MaxOutputTokens, MaxSteps: cfg.Tools.AgentMaxSteps, Context: agentruntimev2.ContextConfig{MaxTokens: cfg.Sessions.MaxContextTokens}}, agentruntimev2.Dependencies{Model: legacy.Model{Client: appruntime.NewLLMClient(cfg)}, Tools: catalog, Policy: hosted.Policy{}, Transcript: hosted.PGTranscript{Pool: stores.PGPool}, Events: v2conv.EventSink()})
		if runnerErr != nil {
			return nil, fmt.Errorf("build hosted v2 runtime: %w", runnerErr)
		}
		v2conv.Agent.Runtime = runner
		v2conv.Redis, v2conv.PodID, v2conv.Lifecycle = stores.Redis, podID, serviceCtx
		v2conv.Locker = stores.Sessions
		v2conv.Format = slack.MarkdownToMrkdwn
		if len(cfg.Security.WorkspaceRoots) > 0 {
			v2conv.Workspace = cfg.Security.WorkspaceRoots[0]
		}
		conv = v2conv
		log.Printf("worker agent runtime: v2 hosted")
	} else {
		log.Printf("worker agent runtime: v1")
	}

	access := safety.NewAccessPolicy(cfg.Security.AllowedUsers, cfg.Security.AllowedChannels)
	handler := &slackhandler.Handler{
		Cfg:       cfg,
		Slack:     slackClient,
		Access:    access,
		Conv:      conv,
		Prompt:    rt.Prompt,
		Metrics:   recorder,
		Runs:      stores.Runs,
		UserPrefs: stores.UserPrefs,
		Home: slackhome.Controller{
			Cfg:    cfg,
			Access: access,
			Slack:  slackClient,
			Store:  stores.UserPrefs,
			Redis:  stores.Redis,
		},
	}
	convV1.WebSearchEnabled = handler.WebSearchPreference
	convV1.Multimodal = multimodal
	convV1.Events = recorder
	if v2conv, ok := conv.(*conversationv2.Service); ok {
		v2conv.ModeForUser = func(userID string) conversationv2.ConversationMode {
			return conversationv2.ConversationMode(handler.Home.ConversationMode(userID))
		}
	}

	s := &Service{
		cfg:       cfg,
		stores:    stores,
		slack:     slackClient,
		runtime:   rt,
		metrics:   recorder,
		health:    healthService,
		reminders: reminder.Scheduler{Store: stores.Reminders, Messenger: slackClient, Redis: stores.Redis},
		conv:      conv,
		handler:   handler,
		ctx:       serviceCtx,
		cancel:    serviceCancel,
		serveErr:  make(chan error, 1),
	}
	s.eventCond = sync.NewCond(&s.eventMu)
	s.slackWorker = &slackevents.Worker{
		Inbox:          stores.Events,
		Redis:          stores.Redis,
		Handler:        handler.Handle,
		Workers:        cfg.HTTP.EventWorkers,
		QueueSize:      cfg.HTTP.EventQueueSize,
		EventTimeout:   cfg.HTTP.EventTimeout,
		InboxLease:     cfg.HTTP.EventInboxLease,
		MaxAttempts:    cfg.HTTP.EventMaxAttempts,
		RetryBase:      cfg.HTTP.EventRetryBase,
		RetryMax:       cfg.HTTP.EventRetryMax,
		IsDraining:     func() bool { return s.draining.Load() },
		BeginEvent:     s.beginEvent,
		EndEvent:       s.endEvent,
		StartGoroutine: s.Go,
		Observer:       recorder,
	}

	cleanup = false
	return s, nil
}

func (s *Service) StartBackground() {
	if s == nil {
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
	if s.handler != nil {
		s.Go(func(ctx context.Context) {
			s.handler.Home.StartRefreshSubscriber(ctx)
		})
	}
	if s.slackWorker != nil {
		s.slackWorker.Start(s.ctx)
	}
	s.Go(s.serveHealth)
}

func (s *Service) RunUntilDone(ctx context.Context) error {
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-s.serveErr:
	}
	s.draining.Store(true)
	if !s.waitEvents(s.cfg.HTTP.ShutdownTimeout) {
		log.Printf("shutdown: timed out waiting for in-flight Slack events")
	}
	if s.cancel != nil {
		s.cancel()
	}
	return runErr
}

func (s *Service) serveHealth(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/readyz", s.handleReady)
	mux.Handle("/metrics", s.metrics)
	mux.HandleFunc("/drain", s.handleDrain)
	server := &http.Server{
		Addr: s.cfg.HTTP.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.HTTP.ShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		select {
		case s.serveErr <- fmt.Errorf("worker health server: %w", err):
		default:
		}
		return
	}
	<-done
}

func (s *Service) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if s.stores == nil || s.stores.PGPool == nil || s.stores.Redis == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.stores.PGPool.Ping(ctx); err != nil {
		http.Error(w, "postgres unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.stores.Redis.Ping(ctx); err != nil {
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ready"))
}

func (s *Service) handleDrain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !httpguard.IsDirectLoopback(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.draining.Store(true)
	_, _ = w.Write([]byte("draining"))
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.Wait(5 * time.Second)
	if s.stores != nil {
		s.stores.Close()
	}
}

func (s *Service) Go(fn func(context.Context)) {
	if fn == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn(s.ctx)
	}()
}

func (s *Service) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	if timeout <= 0 {
		<-done
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (s *Service) waitEvents(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	for s.activeEvents > 0 {
		if timeout <= 0 {
			s.eventCond.Wait()
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		timer := time.AfterFunc(remaining, func() {
			s.eventMu.Lock()
			s.eventCond.Broadcast()
			s.eventMu.Unlock()
		})
		s.eventCond.Wait()
		timer.Stop()
	}
	return true
}

func (s *Service) beginEvent() bool {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.draining.Load() {
		return false
	}
	s.activeEvents++
	return true
}

func (s *Service) endEvent() {
	s.eventMu.Lock()
	if s.activeEvents > 0 {
		s.activeEvents--
	}
	s.eventCond.Broadcast()
	s.eventMu.Unlock()
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

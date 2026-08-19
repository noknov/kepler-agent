package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
	"github.com/noknov/slack-copilot-agent/packages/appsupport"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/connections"
	"github.com/noknov/slack-copilot-agent/packages/health"
	"github.com/noknov/slack-copilot-agent/packages/infra/httpguard"
	sharedlogging "github.com/noknov/slack-copilot-agent/packages/infra/logging"
	"github.com/noknov/slack-copilot-agent/packages/infra/telemetry"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	"github.com/noknov/slack-copilot-agent/packages/platform"
	"github.com/noknov/slack-copilot-agent/packages/profiles/hosted"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
	"github.com/noknov/slack-copilot-agent/packages/reminder"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/agent"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/events"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/handler"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/home"
	slackmessaging "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/messaging"
	slackTools "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/tools"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/gitcache"
	hostedTools "github.com/noknov/slack-copilot-agent/packages/tools/hosted"
)

type Service struct {
	cfg         config.Config
	stores      *platform.Stores
	slack       *slack.Client
	metrics     *observability.Recorder
	health      *health.Service
	reminders   reminder.Scheduler
	conv        slackconversation.ControlledConversation
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
	shutdownTelemetry, err := telemetry.Setup(ctx, "slack-copilot-worker")
	if err != nil {
		return fmt.Errorf("configure telemetry: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTelemetry(shutdownCtx)
	}()
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
	podID := appsupport.GeneratePodID()
	rates := hosted.CostRates(cfg)
	recorder.SetCostRates(rates)
	runSink := &hosted.RunSink{Store: stores.Runs, Provider: cfg.LLM.Provider, Model: cfg.LLM.Model, Rates: rates, Metrics: recorder}
	if err := runSink.Recover(ctx, stores.PGPool); err != nil {
		return nil, fmt.Errorf("recover agent run projections: %w", err)
	}
	events := transcript.NewFanout(runSink)
	workspacePolicy := safety.WorkspacePolicy{Roots: cfg.Security.WorkspaceRoots}
	connStore := connections.PGStore{Pool: stores.PGPool, SecretKey: cfg.Connections.EncryptionKey}
	connService := connections.NewServiceFromConfig(connStore, cfg)
	surface := hostedTools.SurfaceOptions{Name: "slack", AvailableDeps: map[string]bool{
		"slack":    slackClient != nil,
		"reminder": stores.Reminders != nil,
	}, Connections: &connService}
	bundle, err := hostedTools.NewCatalog(cfg, workspacePolicy, safety.NewCommandPolicy(), stores.UserPrefs, surface)
	if err != nil {
		return nil, fmt.Errorf("build hosted tool catalog: %w", err)
	}
	catalog := bundle.Catalog
	slackTools.AddToCatalog(catalog, hostedTools.PolicyForSurface(cfg, surface), cfg, slackClient, stores.Reminders, stores.Redis, &connService)
	profile, profileErr := hosted.NewProfile(cfg, hosted.ProfileDependencies{
		Tools: catalog, Postgres: stores.PGPool, Redis: stores.Redis, ToolSpills: stores.Runs, Events: events, Metrics: recorder,
	})
	if profileErr != nil {
		return nil, fmt.Errorf("build hosted profile: %w", profileErr)
	}
	healthService := health.NewService(profile.Tools, cfg.Security.WorkspaceRoots)
	healthService.Redis = stores.Redis
	conversation := slackagent.New(profile.Agent, slackClient, profile.Prompt, profile.Redactor, stores.UserPrefs)
	conversation.ThreadLoader = slackmessaging.ThreadLoader{Bot: slackClient}
	if bundle.ClickStack != nil {
		policy := hostedTools.PolicyForSurface(cfg, surface)
		conversation.BeforeRun = func(ctx context.Context, userID string) error {
			return bundle.ClickStack.Ensure(ctx, catalog, policy, userID)
		}
	}
	conversation.OnDelivered = runSink.LinkSlackMessage
	conversation.AlreadyDelivered = runSink.SlackMessageDelivered
	if profile.SecondaryModel != nil {
		conversation.Progress = &slackagent.ProgressSummarizer{Client: profile.SecondaryModel, Model: profile.SecondaryModelName, Sanitize: profile.Redactor.Sanitize, ToolDescriptions: toolDescriptions(profile.Tools)}
	}
	conversation.Redis, conversation.PodID, conversation.Lifecycle = stores.Redis, podID, serviceCtx
	conversation.Inputs = stores.Inputs
	conversation.Locker = stores.Sessions
	if len(cfg.Security.WorkspaceRoots) > 0 {
		conversation.Workspace = cfg.Security.WorkspaceRoots[0]
	}
	multimodal := multimodalPredicate(cfg.LLM.MultimodalModels)
	conversation.Multimodal = multimodal
	conversation.MultimodalModel = func() string { return cfg.LLM.MultimodalModel }
	conversation.ModelFor = func(req slackconversation.Request) string {
		for _, content := range req.Content {
			if content.Type == model.ContentImage && multimodal != nil && !multimodal(cfg.LLM.Model) && cfg.LLM.MultimodalModel != "" {
				return cfg.LLM.MultimodalModel
			}
		}
		return cfg.LLM.Model
	}
	events.Add(conversation.EventSink())
	conv := slackconversation.ControlledConversation(conversation)
	log.Printf("worker agent runtime: shared")

	access := safety.NewAccessPolicy(cfg.Security.AllowedUsers, cfg.Security.AllowedChannels)
	handler := &slackhandler.Handler{
		Cfg:       cfg,
		Slack:     slackClient,
		Access:    access,
		Conv:      conv,
		Prompt:    profile.Prompt,
		Metrics:   recorder,
		Runs:      stores.Runs,
		UserPrefs: stores.UserPrefs,
		Home: slackhome.Controller{
			Cfg:         cfg,
			Access:      access,
			Slack:       slackClient,
			Store:       stores.UserPrefs,
			Redis:       stores.Redis,
			Connections: connService,
		},
	}
	conversation.WebSearchEnabled = handler.WebSearchPreference
	conversation.ModeForUser = func(userID string) slackagent.ConversationMode {
		return slackagent.ConversationMode(handler.Home.ConversationMode(userID))
	}

	s := &Service{
		cfg:       cfg,
		stores:    stores,
		slack:     slackClient,
		metrics:   recorder,
		health:    healthService,
		reminders: reminder.Scheduler{Store: stores.Reminders, Messenger: slackmessaging.BotUserMessenger{Client: slackClient}, Redis: stores.Redis},
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

func toolDescriptions(catalog interface{ Descriptors() []tool.Descriptor }) map[string]string {
	if catalog == nil {
		return nil
	}
	descriptions := make(map[string]string)
	for _, descriptor := range catalog.Descriptors() {
		name := strings.TrimSpace(descriptor.Name)
		description := strings.TrimSpace(descriptor.Description)
		if name != "" && description != "" {
			descriptions[name] = description
		}
	}
	if len(descriptions) == 0 {
		return nil
	}
	return descriptions
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
	mmSet := make(map[string]bool, len(models))
	for _, m := range models {
		mmSet[m] = true
	}
	return func(model string) bool {
		return mmSet[model]
	}
}

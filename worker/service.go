package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/appsupport"
	"github.com/noknov/kepler-agent/packages/cloud"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/health"
	"github.com/noknov/kepler-agent/packages/infra/httpguard"
	sharedlogging "github.com/noknov/kepler-agent/packages/infra/logging"
	"github.com/noknov/kepler-agent/packages/infra/telemetry"
	"github.com/noknov/kepler-agent/packages/memory"
	"github.com/noknov/kepler-agent/packages/observability"
	"github.com/noknov/kepler-agent/packages/platform"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/prompts"
	"github.com/noknov/kepler-agent/packages/providers"
	"github.com/noknov/kepler-agent/packages/safety"
	slackagent "github.com/noknov/kepler-agent/packages/surfaces/slack/agent"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	slackevents "github.com/noknov/kepler-agent/packages/surfaces/slack/events"
	slackhandler "github.com/noknov/kepler-agent/packages/surfaces/slack/handler"
	slackhome "github.com/noknov/kepler-agent/packages/surfaces/slack/home"
	slackmessaging "github.com/noknov/kepler-agent/packages/surfaces/slack/messaging"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/taskspace"
	slackTools "github.com/noknov/kepler-agent/packages/surfaces/slack/tools"
	"github.com/noknov/kepler-agent/packages/toolkit/gitcache"
	hostedTools "github.com/noknov/kepler-agent/packages/tools/hosted"
)

type Service struct {
	cfg         config.Config
	stores      *platform.Stores
	slack       *slack.Client
	metrics     *observability.Recorder
	health      *health.Service
	conv        slackconversation.ControlledConversation
	handler     *slackhandler.Handler
	slackWorker *slackevents.Worker
	runSink     *hosted.RunSink
	runEvents   *transcript.AsyncSink

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
	shutdownTelemetry, err := telemetry.Setup(ctx, "kepler-agent-worker")
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
	if err := prompts.LoadFromEnv(); err != nil {
		return nil, fmt.Errorf("load prompts: %w", err)
	}
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

	if cfg.Memory.Embedding.Enabled {
		checkCtx, cancel := context.WithTimeout(ctx, cfg.Memory.Embedding.Timeout)
		err := (memory.PGStore{Pool: stores.PGPool}).CheckSemanticSchema(checkCtx)
		cancel()
		if err != nil {
			return nil, err
		}
	}

	slackClient := slack.NewClient(cfg.Slack.BotToken, cfg.Slack.BotUserID)
	if botUserID, err := slackClient.AuthTest(context.Background()); err == nil {
		if cfg.Slack.BotUserID == "" {
			slackClient.SetBotUserID(botUserID)
			cfg.Slack.BotUserID = botUserID
			log.Printf("resolved Slack bot user id with auth.test")
		}
	} else {
		log.Printf("warning: slack auth.test failed: %v", err)
	}

	recorder := observability.NewRecorder()
	modelGovernor := model.NewGovernor(cfg.Tools.AgentMaxConcurrentModelCalls)
	podID := appsupport.GeneratePodID()
	rates := hosted.CostRates(cfg)
	recorder.SetCostRates(rates)
	runSink := &hosted.RunSink{Store: stores.Runs, Provider: cfg.LLM.Provider, Model: cfg.LLM.Model, Rates: rates, Metrics: recorder}
	if err := runSink.Recover(ctx, stores.PGPool); err != nil {
		return nil, fmt.Errorf("recover agent run projections: %w", err)
	}
	// Run records are rebuildable transcript projections. Keep them off the
	// latency-sensitive Slack/model event path.
	runEvents := transcript.NewAsyncSink(serviceCtx, runSink, 2048)
	events := transcript.NewFanout(runEvents)
	workspacePolicy := safety.WorkspacePolicy{Roots: cfg.Security.WorkspaceRoots}
	connStore := connections.PGStore{Pool: stores.PGPool, SecretKey: cfg.Connections.EncryptionKey}
	connService := connections.NewServiceFromConfig(connStore, cfg)
	connService.OAuthCompleted = connections.RedisOAuthCompletionNotifier{Redis: stores.Redis}
	continuations := slackagent.NewRedisContinuationStore(stores.Redis)
	surface := hostedTools.SurfaceOptions{Name: "slack", AvailableDeps: map[string]bool{
		"slack": slackClient != nil,
	}, Connections: &connService}
	bundle, err := hostedTools.NewCatalog(cfg, workspacePolicy, stores.UserPrefs, surface)
	if err != nil {
		return nil, fmt.Errorf("build hosted tool catalog: %w", err)
	}
	catalog := bundle.Catalog
	access := safety.NewAccessPolicy(cfg.Security.AllowedUsers, cfg.Security.AllowedChannels)
	var spaces *taskspace.Service
	if cfg.Slack.CodeChannelsEnabled {
		spaceStore := taskspace.PGStore{Pool: stores.PGPool}
		if err := spaceStore.CheckSchema(ctx); err != nil {
			return nil, err
		}
		spaces = &taskspace.Service{Store: spaceStore, Slack: slackClient, Access: access, BotUserID: cfg.Slack.BotUserID}
		if err := slackTools.AddTaskSpaceTools(catalog, hostedTools.PolicyForSurface(cfg, surface), spaces); err != nil {
			return nil, err
		}
	}

	if err := slackTools.AddToCatalog(catalog, hostedTools.PolicyForSurface(cfg, surface), cfg, slackClient, &connService); err != nil {
		return nil, fmt.Errorf("register Slack tools: %w", err)
	}
	profile, profileErr := hosted.NewProfile(cfg, hosted.ProfileDependencies{
		ModelGovernor: modelGovernor,
		Tools:         catalog, Postgres: stores.PGPool, Redis: stores.Redis, ToolSpills: stores.Runs, Events: events, Metrics: recorder, Lease: stores.Sessions,
		ConnectionContinuations: slackagent.RuntimeConnectionContinuationStore{Store: continuations},
	})
	if profileErr != nil {
		return nil, fmt.Errorf("build hosted profile: %w", profileErr)
	}
	healthService := health.NewService(profile.Tools, cfg.Security.WorkspaceRoots)
	healthService.Redis = stores.Redis
	conversation := slackagent.New(profile.Agent, slackClient, profile.Prompt, profile.Redactor, stores.UserPrefs)
	conversation.ApprovalNotifyUserID = cfg.Integrations.GitHub.ApprovalNotifyUserID
	conversation.ThreadLoader = slackmessaging.ThreadLoader{Bot: slackClient}
	policy := hostedTools.PolicyForSurface(cfg, surface)
	var beforeRuns []func(context.Context, string) error
	if bundle.ClickStack != nil {
		clickstackReg := bundle.ClickStack
		beforeRuns = append(beforeRuns, func(ctx context.Context, userID string) error {
			return clickstackReg.Ensure(ctx, catalog, policy, userID)
		})
	}
	if bundle.Notion != nil {
		notionReg := bundle.Notion
		beforeRuns = append(beforeRuns, func(ctx context.Context, userID string) error {
			return notionReg.Ensure(ctx, catalog, policy, userID)
		})
	}
	if len(beforeRuns) > 0 {
		conversation.BeforeRun = func(ctx context.Context, userID string) error {
			for _, run := range beforeRuns {
				if err := run(ctx, userID); err != nil {
					return err
				}
			}
			return nil
		}
	}
	conversation.Deliveries = slackagent.PGDeliveryStore{Pool: stores.PGPool}
	conversation.OnDelivered = runSink.LinkSlackMessage
	conversation.Redis, conversation.PodID = stores.Redis, podID
	conversation.RunTimeout = cfg.Tools.AgentTurnTimeout
	conversation.MaxConcurrentRuns = cfg.HTTP.EventWorkers
	conversation.Tools = profile.Tools
	conversation.Continuations = continuations
	conversation.Connections = &connService
	conversation.Inputs = stores.Inputs
	if len(cfg.Security.WorkspaceRoots) > 0 {
		conversation.Workspace = cfg.Security.WorkspaceRoots[0]
	}
	conversation.Model = cfg.LLM.Model
	multimodal := func(ctx context.Context, modelName string) (bool, error) {
		capabilities, err := profile.Capabilities(ctx, modelName)
		return capabilities.SupportsInput(providers.ModalityImage), err
	}
	conversation.Multimodal = multimodal
	events.Add(conversation.EventSink())
	conv := slackconversation.ControlledConversation(conversation)
	log.Printf("worker agent runtime: shared")

	handler := &slackhandler.Handler{
		Spaces:    spaces,
		Approvals: agentruntime.ApprovalAuthority{Transcript: hosted.PGTranscript{Pool: stores.PGPool}},
		Cfg:       cfg,
		Slack:     slackClient,
		Access:    access,
		Conv:      conv,
		Prompt:    profile.Prompt,
		Metrics:   recorder,
		Runs:      stores.Runs,
		UserPrefs: stores.UserPrefs,
		Home: slackhome.Controller{
			Memory:      memory.PGStore{Pool: stores.PGPool},
			Cfg:         cfg,
			Access:      access,
			Slack:       slackClient,
			Store:       stores.UserPrefs,
			Redis:       stores.Redis,
			Connections: connService,
		},
	}
	conversation.AuthorizeInput = handler.AuthorizeInput
	conversation.WebSearchEnabled = handler.WebSearchPreference
	conversation.ModeForUser = func(userID string) slackagent.ConversationMode {
		return slackagent.ConversationMode(handler.Home.ConversationMode(userID))
	}
	connService.OnConnectionChanged = func(ctx context.Context, userID, provider string) error {
		return handler.Home.RequestRefresh(ctx, userID)
	}

	s := &Service{
		cfg:       cfg,
		stores:    stores,
		slack:     slackClient,
		metrics:   recorder,
		health:    healthService,
		conv:      conv,
		handler:   handler,
		ctx:       serviceCtx,
		cancel:    serviceCancel,
		serveErr:  make(chan error, 1),
		runSink:   runSink,
		runEvents: runEvents,
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
	s.Go(s.recoverRunProjections)
	s.Go(s.pruneEphemeralData)
	s.Go(func(ctx context.Context) {
		s.conv.StartControlSubscriber(ctx)
	})
	s.Go(func(ctx context.Context) {
		s.conv.StartConnectionCompletedSubscriber(ctx)
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

const ephemeralPrunePeriod = 6 * time.Hour

// pruneEphemeralData bounds operational queues and expired authentication
// records. Conversation transcripts and run evidence are retained until an
// explicit product retention policy exists; deleting those implicitly would
// make old conversations unreplayable.
func (s *Service) pruneEphemeralData(ctx context.Context) {
	if s == nil || s.stores == nil || s.stores.PGPool == nil {
		return
	}
	prune := func() {
		pruneCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		statements := []string{
			`DELETE FROM slack_event_inbox WHERE status='completed' AND completed_at < NOW()-INTERVAL '30 days'`,
			`DELETE FROM slack_event_inbox WHERE status='dead_letter' AND dead_lettered_at < NOW()-INTERVAL '90 days'`,
			`DELETE FROM agent_session_inputs WHERE acknowledged_at < NOW()-INTERVAL '7 days'`,
			`DELETE FROM oauth_states WHERE expires_at < NOW()-INTERVAL '1 day'`,
		}
		for _, statement := range statements {
			if _, err := s.stores.PGPool.Exec(pruneCtx, statement); err != nil {
				log.Printf("prune ephemeral data: %v", err)
				return
			}
		}
	}
	prune()
	ticker := time.NewTicker(ephemeralPrunePeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

const projectionRecoveryPeriod = 5 * time.Minute

// recoverRunProjections closes the gap created when the non-authoritative run
// projection is saturated. The canonical transcript stays complete; this
// worker replays it periodically and after AsyncSink sheds an event, so
// dashboard/cost state becomes eventually consistent without a restart.
func (s *Service) recoverRunProjections(ctx context.Context) {
	if s == nil || s.runSink == nil || s.stores == nil || s.stores.PGPool == nil {
		return
	}
	ticker := time.NewTicker(projectionRecoveryPeriod)
	defer ticker.Stop()
	var shed <-chan struct{}
	if s.runEvents != nil {
		shed = s.runEvents.Shed()
	}
	var observedDrops uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-shed:
		}
		dropped := uint64(0)
		if s.runEvents != nil {
			dropped = s.runEvents.Dropped()
		}
		if dropped > observedDrops {
			delta := dropped - observedDrops
			log.Printf("run projection shed %d events; replaying canonical transcript", delta)
			if s.metrics != nil {
				s.metrics.AddEvent("async_sink_dropped", int64(delta), map[string]any{"projection": "agent_runs"})
			}
		}
		if err := s.runSink.Recover(ctx, s.stores.PGPool); err != nil {
			log.Printf("recover agent run projections: %v", err)
			continue
		}
		observedDrops = dropped
	}
}

func (s *Service) RunUntilDone(ctx context.Context) error {
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-s.serveErr:
	}
	s.draining.Store(true)
	// All ingress surfaces share one grace deadline. Exhaustion cancels service
	// work; queue owners retain their existing bounded terminal/claim cleanup.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), s.cfg.HTTP.ShutdownTimeout)
	defer drainCancel()
	if !s.waitEvents(drainCtx) {
		log.Printf("shutdown: timed out waiting for in-flight Slack events")
	}
	if s.conv != nil {
		s.conv.Quiesce()
		if !s.conv.WaitIdle(drainCtx) {
			log.Printf("shutdown: timed out waiting for Slack execution")
		}
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
	mux.Handle("/metrics/prometheus", s.metrics.PrometheusHandler())
	mux.HandleFunc("/drain", s.handleDrain)
	if err := cloud.RegisterWorker(mux, s.cfg); err != nil {
		select {
		case s.serveErr <- err:
		default:
		}
		return
	}
	server := &http.Server{
		Addr:              s.cfg.HTTP.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Minute,
		IdleTimeout:       120 * time.Second,
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
	if s.conv != nil {
		s.conv.Quiesce()
	}
	_, _ = w.Write([]byte("draining"))
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	s.waitContext(cleanupCtx)
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
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return s.waitContext(ctx)
}
func (s *Service) waitContext(ctx context.Context) bool {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Service) waitEvents(ctx context.Context) bool {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.activeEvents == 0 {
		return true
	}
	stop := context.AfterFunc(ctx, func() { s.eventMu.Lock(); s.eventCond.Broadcast(); s.eventMu.Unlock() })
	defer stop()
	for s.activeEvents > 0 {
		if ctx.Err() != nil {
			return false
		}
		s.eventCond.Wait()
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

package worker

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/appsupport"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/conversation"
	"github.com/noknov/slack-copilot-agent/packages/health"
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

type Service struct {
	cfg         config.Config
	stores      *platform.Stores
	slack       *slack.Client
	runtime     appruntime.AgentRuntime
	metrics     *observability.Recorder
	health      *health.Service
	reminders   reminder.Scheduler
	conv        *conversation.Service
	handler     *slackhandler.Handler
	slackWorker *slackevents.Worker

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	eventMu      sync.Mutex
	eventCond    *sync.Cond
	activeEvents int
	draining     atomic.Bool
}

func Run(ctx context.Context) error {
	cfg, err := config.LoadFor(config.ProfileSlackWorker)
	if err != nil {
		return err
	}
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
	rt := appruntime.NewAgentRuntime(cfg, slackClient, stores.Reminders, recorder, stores.Redis)
	recorder.SetCostRates(rt.CostRates)

	healthService := health.NewService(rt.Tools, cfg.Security.WorkspaceRoots)
	healthService.Redis = stores.Redis

	conv := conversation.NewService(stores.Sessions, slackClient, rt.Runner, rt.Memory, rt.Prompt, rt.Redactor, recorder)
	runSemaphore := make(chan struct{}, cfg.Tools.AgentMaxConcurrentRuns)
	conv.RunSemaphore = runSemaphore
	conv.MaxConcurrentRuns = cfg.Tools.AgentMaxConcurrentRuns
	conv.Redis = stores.Redis
	conv.PodID = appsupport.GeneratePodID()
	conv.FollowUpContext = serviceCtx
	conv.Format = slack.MarkdownToMrkdwn
	conv.RunStore = stores.Runs
	conv.ToolSpillStore = stores.Runs
	conv.RunProvider = cfg.LLM.Provider
	conv.RunModel = cfg.LLM.Model
	multimodal := multimodalPredicate(cfg.LLM.MultimodalModels)
	conv.ModelRouter = conversation.ModelRouter{
		DefaultModel:            cfg.LLM.Model,
		MultimodalFallbackModel: cfg.LLM.MultimodalModel,
		SupportsMultimodal:      multimodal,
	}
	conv.CostRates = rt.CostRates
	conv.HealthSummary = healthService.SummaryPrompt
	if cfg.Integrations.TTS.Auto && cfg.Integrations.TTS.APIKey != "" {
		conv.AutoTTS = appsupport.NewAutoTTSFunc(cfg, slackClient)
		conv.TTSSummarizer = appsupport.NewTTSSummarizer(cfg, rt)
	}

	access := safety.NewAccessPolicy(cfg.Security.AllowedUsers, cfg.Security.AllowedChannels)
	handler := &slackhandler.Handler{
		Cfg:     cfg,
		Slack:   slackClient,
		Access:  access,
		Conv:    conv,
		Prompt:  rt.Prompt,
		Metrics: recorder,
		Runs:    stores.Runs,
		Home: slackhome.Controller{
			Cfg:    cfg,
			Access: access,
			Redis:  stores.Redis,
			Slack:  slackClient,
		},
	}
	conv.WebSearchEnabled = handler.WebSearchPreference
	conv.Multimodal = multimodal

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
		IsDraining:     func() bool { return s.draining.Load() },
		BeginEvent:     s.beginEvent,
		EndEvent:       s.endEvent,
		StartGoroutine: s.Go,
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
	if s.slackWorker != nil {
		s.slackWorker.Start(s.ctx)
	}
}

func (s *Service) RunUntilDone(ctx context.Context) error {
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

package gateway

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/cloud"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/infra/httpguard"
	sharedlogging "github.com/noknov/kepler-agent/packages/infra/logging"
	"github.com/noknov/kepler-agent/packages/infra/telemetry"
	"github.com/noknov/kepler-agent/packages/memory"
	"github.com/noknov/kepler-agent/packages/platform"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	slackevents "github.com/noknov/kepler-agent/packages/surfaces/slack/events"
	slackgateway "github.com/noknov/kepler-agent/packages/surfaces/slack/gateway"
	slackhandler "github.com/noknov/kepler-agent/packages/surfaces/slack/handler"
	slackhome "github.com/noknov/kepler-agent/packages/surfaces/slack/home"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/taskspace"
)

type Service struct {
	cfg         config.Config
	stores      *platform.EventIngressStores
	gateway     slackgateway.Gateway
	home        slackhome.Controller
	connections connections.Service
	draining    atomic.Bool
}

func Run(ctx context.Context) error {
	cfg, err := config.LoadFor(config.ProfileGateway)
	if err != nil {
		return err
	}
	sharedlogging.Configure(cfg.Observing.LogLevel)
	shutdownTelemetry, err := telemetry.Setup(ctx, "kepler-agent-gateway")
	if err != nil {
		return err
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
	return service.ListenAndServe(ctx)
}

func New(ctx context.Context, cfg config.Config) (*Service, error) {
	stores, err := platform.NewEventIngressStores(ctx, cfg.Storage)
	if err != nil {
		return nil, err
	}
	var slackClient *slack.Client
	if cfg.Slack.BotToken != "" {
		slackClient = slack.NewClient(cfg.Slack.BotToken, cfg.Slack.BotUserID)
	}
	service, err := newWithStores(ctx, cfg, stores, slackClient)
	if err != nil {
		stores.Close()
	}
	return service, err
}

// newWithStores composes the same ingress for production and isolated tests;
// only I/O clients are substituted, never approval or input-admission policy.
func newWithStores(ctx context.Context, cfg config.Config, stores *platform.EventIngressStores, slackClient *slack.Client) (*Service, error) {
	s := &Service{cfg: cfg, stores: stores}
	connStore := connections.PGStore{Pool: stores.PGPool, SecretKey: cfg.Connections.EncryptionKey}
	connService := connections.NewServiceFromConfig(connStore, cfg)
	connService.OAuthCompleted = connections.RedisOAuthCompletionNotifier{Redis: stores.Redis}
	access := safety.NewAccessPolicy(cfg.Security.AllowedUsers, cfg.Security.AllowedChannels)
	var spaces *taskspace.Service
	if cfg.Slack.CodeChannelsEnabled {
		var err error
		spaces, err = taskspace.NewService(ctx, stores.PGPool, slackClient, access)
		if err != nil {
			return nil, err
		}
	}
	s.home = slackhome.Controller{
		Memory:      memory.PGStore{Pool: stores.PGPool},
		Cfg:         cfg,
		Access:      access,
		Slack:       slackClient,
		Store:       stores.UserPrefs,
		Redis:       stores.Redis,
		Connections: connService,
	}
	connService.OnConnectionChanged = func(ctx context.Context, userID, provider string) error {
		return s.home.RequestRefresh(ctx, userID)
	}
	s.connections = connService
	handler := &slackhandler.Handler{
		Spaces:    spaces,
		Approvals: agentruntime.ApprovalAuthority{Transcript: hosted.PGTranscript{Pool: stores.PGPool}},
		Access:    access,
		Cfg:       cfg,
		Slack:     slackClient,
		Home:      s.home,
		UserPrefs: stores.UserPrefs,
		Inputs:    stores.Inputs,
		NotifyApproval: func(ctx context.Context, sessionID string) {
			_ = stores.Redis.Publish(ctx, "agent:approval", sessionID)
		},
	}
	s.gateway = slackgateway.Gateway{
		SigningSecret: cfg.Slack.SigningSecret,
		Inbox:         stores.Events,
		IsDraining:    func() bool { return s.draining.Load() },
		Publish: func(ctx context.Context, eventID string) {
			_ = stores.Redis.Publish(ctx, slackevents.RedisEventChannel, eventID)
		},
		OnInteraction: handler.HandleInteraction,
		WriteError:    s.writeHTTPError,
	}
	return s, nil
}

func (s *Service) Close() {
	if s != nil && s.stores != nil {
		s.stores.Close()
	}
}

func (s *Service) ListenAndServe(ctx context.Context) error {
	handler, err := s.routes()
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              s.cfg.HTTP.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		s.draining.Store(true)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.HTTP.ShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("kepler-agent gateway listening on %s", s.cfg.HTTP.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if ctx.Err() != nil {
		<-shutdownDone
	}
	return nil
}

func (s *Service) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}
	if s.stores == nil || s.stores.PGPool == nil || s.stores.Redis == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.stores.PGPool.Ping(ctx); err != nil {
		http.Error(w, "postgres unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.stores.Redis.Ping(ctx); err != nil {
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

func (s *Service) handleDrain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	if !httpguard.IsDirectLoopback(r) {
		s.writeHTTPError(w, r, http.StatusForbidden, "forbidden", nil)
		return
	}
	s.draining.Store(true)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("draining"))
}

func (s *Service) writeHTTPError(w http.ResponseWriter, r *http.Request, status int, message string, err error) {
	if err != nil {
		log.Printf("gateway http error method=%s path=%s status=%d remote=%s msg=%q err=%v", r.Method, r.URL.Path, status, r.RemoteAddr, message, err)
	} else {
		log.Printf("gateway http warning method=%s path=%s status=%d remote=%s msg=%q", r.Method, r.URL.Path, status, r.RemoteAddr, message)
	}
	http.Error(w, message, status)
}

func ok(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}

func (s *Service) routes() (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", ok("ok"))
	mux.HandleFunc("/healthz", ok("ok"))
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc("/drain", s.handleDrain)
	mux.HandleFunc("/slack/events", s.gateway.HandleEvents)
	mux.HandleFunc("/slack/interactions", s.gateway.HandleInteractions)
	if s.connections.Config.OAuthEnabled() && s.connections.Config.PublicBaseURL != "" {
		mux.Handle("/oauth/", connections.NewHTTPHandler(s.connections))
	}
	if benchmark, err := cloud.NewGateway(s.cfg, s.stores.Redis); err != nil {
		return nil, err
	} else {
		benchmark.Register(mux)
	}

	return mux, nil
}

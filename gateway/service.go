package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/infra/httpguard"
	sharedlogging "github.com/noknov/kepler-agent/packages/infra/logging"
	"github.com/noknov/kepler-agent/packages/infra/telemetry"
	"github.com/noknov/kepler-agent/packages/platform"
	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/events"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/gateway"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/handler"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/home"
)

type Service struct {
	cfg         config.Config
	stores      *platform.EventIngressStores
	gateway     slackgateway.Gateway
	home        slackhome.Controller
	connections connections.Service
	webProxy    http.Handler
	draining    atomic.Bool
	webMu       sync.Mutex
	webCancels  map[uint64]context.CancelFunc
	webSequence atomic.Uint64
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
	s := &Service{cfg: cfg, stores: stores, webCancels: make(map[uint64]context.CancelFunc)}
	var slackClient *slack.Client
	if cfg.Slack.BotToken != "" {
		slackClient = slack.NewClient(cfg.Slack.BotToken, cfg.Slack.BotUserID)
	}
	connStore := connections.PGStore{Pool: stores.PGPool, SecretKey: cfg.Connections.EncryptionKey}
	continuations := connections.NewRedisContinuationStore(stores.Redis)
	connService := connections.NewServiceFromConfig(connStore, cfg)
	connService.Continuations = continuations
	s.home = slackhome.Controller{
		Cfg:         cfg,
		Access:      safety.NewAccessPolicy(cfg.Security.AllowedUsers, cfg.Security.AllowedChannels),
		Slack:       slackClient,
		Store:       stores.UserPrefs,
		Redis:       stores.Redis,
		Connections: connService,
	}
	connService.OnOAuthCompleted = func(ctx context.Context, userID, provider string) error {
		return s.home.RequestRefresh(ctx, userID)
	}
	s.connections = connService
	if cfg.Web.Enabled {
		upstream, err := url.Parse(cfg.Web.UpstreamURL)
		if err != nil || upstream.Scheme == "" || upstream.Host == "" {
			return nil, fmt.Errorf("invalid WEB_UPSTREAM_URL %q", cfg.Web.UpstreamURL)
		}
		proxy := httputil.NewSingleHostReverseProxy(upstream)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			s.writeHTTPError(w, r, http.StatusBadGateway, "web service unavailable", err)
		}
		s.webProxy = proxy
	}
	handler := &slackhandler.Handler{
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
	if s.webProxy != nil {
		mux.HandleFunc("/", s.serveWeb)
	}

	server := &http.Server{
		Addr:              s.cfg.HTTP.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		s.draining.Store(true)
		// Browser event streams are intentionally long-lived. Disconnect only
		// these proxied requests so a rollout cannot wait for an SSE client;
		// Slack ingress requests retain the configured graceful shutdown budget.
		s.cancelWebRequests()
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

func (s *Service) serveWeb(w http.ResponseWriter, r *http.Request) {
	if s.webProxy == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	id := s.webSequence.Add(1)
	s.webMu.Lock()
	s.webCancels[id] = cancel
	s.webMu.Unlock()
	defer func() {
		s.webMu.Lock()
		delete(s.webCancels, id)
		s.webMu.Unlock()
		cancel()
	}()
	s.webProxy.ServeHTTP(w, r.WithContext(ctx))
}

func (s *Service) cancelWebRequests() {
	s.webMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.webCancels))
	for _, cancel := range s.webCancels {
		cancels = append(cancels, cancel)
	}
	s.webMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *Service) handleReady(w http.ResponseWriter, _ *http.Request) {
	if s.draining.Load() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}
	if s.stores == nil || s.stores.Events == nil || s.stores.Redis == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
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

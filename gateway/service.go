package gateway

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/infra/httpguard"
	sharedlogging "github.com/noknov/slack-copilot-agent/packages/infra/logging"
	"github.com/noknov/slack-copilot-agent/packages/platform"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/slack"
	"github.com/noknov/slack-copilot-agent/packages/slackevents"
	"github.com/noknov/slack-copilot-agent/packages/slackgateway"
	"github.com/noknov/slack-copilot-agent/packages/slackhandler"
	"github.com/noknov/slack-copilot-agent/packages/slackhome"
)

type Service struct {
	cfg      config.Config
	stores   *platform.EventIngressStores
	gateway  slackgateway.Gateway
	home     slackhome.Controller
	draining atomic.Bool
}

func Run(ctx context.Context) error {
	cfg, err := config.LoadFor(config.ProfileGateway)
	if err != nil {
		return err
	}
	sharedlogging.Configure(cfg.Observing.LogLevel)
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
	s := &Service{cfg: cfg, stores: stores}
	var slackClient *slack.Client
	if cfg.Slack.BotToken != "" {
		slackClient = slack.NewClient(cfg.Slack.BotToken, cfg.Slack.BotUserID)
	}
	s.home = slackhome.Controller{
		Cfg:    cfg,
		Access: safety.NewAccessPolicy(cfg.Security.AllowedUsers, cfg.Security.AllowedChannels),
		Slack:  slackClient,
		Store:  stores.UserPrefs,
		Redis:  stores.Redis,
	}
	handler := &slackhandler.Handler{
		Cfg:       cfg,
		Slack:     slackClient,
		Home:      s.home,
		UserPrefs: stores.UserPrefs,
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.HTTP.ShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("slack-copilot gateway listening on %s", s.cfg.HTTP.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if ctx.Err() != nil {
		<-shutdownDone
	}
	return nil
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

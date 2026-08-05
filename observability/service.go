package observabilitysvc

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/health"
	"github.com/noknov/slack-copilot-agent/packages/infra/httpguard"
	sharedlogging "github.com/noknov/slack-copilot-agent/packages/infra/logging"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	"github.com/noknov/slack-copilot-agent/packages/platform"
	"github.com/noknov/slack-copilot-agent/packages/runtime"
)

type Service struct {
	cfg      config.Config
	stores   *platform.Stores
	metrics  *observability.Recorder
	health   *health.Service
	draining bool
	mu       sync.RWMutex
}

func Run(ctx context.Context) error {
	cfg, err := config.LoadFor(config.ProfileObservability)
	if err != nil {
		return err
	}
	sharedlogging.Configure(cfg.Observing.LogLevel)
	service, err := New(ctx, cfg)
	if err != nil {
		return err
	}
	defer service.Close()
	service.Start(ctx)
	return service.ListenAndServe(ctx)
}

func New(ctx context.Context, cfg config.Config) (*Service, error) {
	stores, err := platform.NewStores(ctx, cfg.Storage)
	if err != nil {
		return nil, err
	}
	recorder := observability.NewRecorder()
	rt := runtime.NewAgentRuntime(cfg, nil, stores.Reminders, recorder, stores.Redis, nil)
	healthService := health.NewService(rt.Tools, cfg.Security.WorkspaceRoots)
	healthService.Redis = stores.Redis
	return &Service{
		cfg:     cfg,
		stores:  stores,
		metrics: recorder,
		health:  healthService,
	}, nil
}

func (s *Service) Start(ctx context.Context) {
	if s.health != nil {
		go s.health.Start(ctx)
	}
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
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/health/dashboard", s.handleHealthDashboard)
	mux.HandleFunc("/health/tools", s.handleToolHealth)
	mux.HandleFunc("/runs", s.handleRuns)
	mux.HandleFunc("/runs/", s.handleRun)

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
		s.setDraining(true)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.HTTP.ShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("slack-copilot observability listening on %s", s.cfg.HTTP.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if ctx.Err() != nil {
		<-shutdownDone
	}
	return nil
}

func (s *Service) handleReady(w http.ResponseWriter, _ *http.Request) {
	if s.isDraining() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}
	if s.stores == nil || s.stores.Runs == nil || s.stores.Redis == nil {
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
	s.setDraining(true)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("draining"))
}

func (s *Service) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	if !s.authorize(r) {
		s.writeHTTPError(w, r, http.StatusForbidden, "forbidden", nil)
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	runsList, err := s.stores.Runs.List(r.Context(), limit)
	if err != nil {
		s.writeHTTPError(w, r, http.StatusInternalServerError, "failed to list runs", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(runsList)
}

func (s *Service) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	if !s.authorize(r) {
		s.writeHTTPError(w, r, http.StatusForbidden, "forbidden", nil)
		return
	}
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	runsList, err := s.stores.Runs.List(r.Context(), limit)
	if err != nil {
		s.writeHTTPError(w, r, http.StatusInternalServerError, "failed to build metrics", err)
		return
	}
	statuses := map[string]int64{}
	toolCalls := map[string]int64{}
	toolErrors := map[string]int64{}
	lastErrors := []string{}
	var usage observability.TokenUsage
	var estimatedCost float64
	var llmCalls int64
	for _, run := range runsList {
		statuses[run.Status]++
		usage.PromptTokens += int64(run.Usage.PromptTokens)
		usage.CompletionTokens += int64(run.Usage.CompletionTokens)
		usage.TotalTokens += int64(run.Usage.TotalTokens)
		usage.CacheReadInputTokens += int64(run.Usage.CacheReadInputTokens)
		usage.CacheCreationInputTokens += int64(run.Usage.CacheCreationInputTokens)
		usage.ReasoningTokens += int64(run.Usage.ReasoningTokens)
		estimatedCost += run.EstimatedCostUSD
		if run.Error != "" {
			lastErrors = append(lastErrors, run.Error)
		}
		for _, step := range run.Steps {
			if step.Type == "llm" {
				llmCalls++
			}
			if step.Type == "tool" && step.Name != "" {
				toolCalls[step.Name]++
				if step.Error != "" {
					toolErrors[step.Name]++
					lastErrors = append(lastErrors, step.Name+": "+step.Error)
				}
			}
		}
	}
	if len(lastErrors) > 20 {
		lastErrors = lastErrors[len(lastErrors)-20:]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"source":                 "agent_runs",
		"window_runs":            len(runsList),
		"run_statuses":           statuses,
		"llm_calls":              llmCalls,
		"llm_usage":              usage,
		"estimated_cost_usd":     estimatedCost,
		"tool_calls":             toolCalls,
		"tool_errors":            toolErrors,
		"last_errors":            lastErrors,
		"process_metrics_notice": "split deployments aggregate /metrics from durable runs; scrape each process separately for live in-memory counters",
	})
}

func (s *Service) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	if !s.authorize(r) {
		s.writeHTTPError(w, r, http.StatusForbidden, "forbidden", nil)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/runs/")
	run, ok, err := s.stores.Runs.Get(r.Context(), id)
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

func (s *Service) handleToolHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	if !s.authorize(r) {
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

func (s *Service) handleHealthDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	if !s.authorize(r) {
		s.writeHTTPError(w, r, http.StatusForbidden, "forbidden", nil)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(healthDashboardHTML))
}

func (s *Service) authorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorize(r) {
			s.writeHTTPError(w, r, http.StatusForbidden, "forbidden", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) authorize(r *http.Request) bool {
	token := strings.TrimSpace(s.cfg.Observing.AdminToken)
	if token == "" {
		return s.cfg.Observing.AllowUnauthenticated && httpguard.IsDirectLoopback(r)
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

func (s *Service) writeHTTPError(w http.ResponseWriter, r *http.Request, status int, message string, err error) {
	if err != nil {
		log.Printf("observability http error method=%s path=%s status=%d remote=%s msg=%q err=%v", r.Method, r.URL.Path, status, r.RemoteAddr, message, err)
		if s.metrics != nil && status >= 500 {
			s.metrics.Error(fmt.Errorf("http %s %s: %s: %w", r.Method, r.URL.Path, message, err))
		}
	} else {
		log.Printf("observability http warning method=%s path=%s status=%d remote=%s msg=%q", r.Method, r.URL.Path, status, r.RemoteAddr, message)
		if s.metrics != nil && status >= 500 {
			s.metrics.Error(fmt.Errorf("http %s %s: %s", r.Method, r.URL.Path, message))
		}
	}
	http.Error(w, message, status)
}

func (s *Service) setDraining(value bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.draining = value
}

func (s *Service) isDraining() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.draining
}

func ok(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}

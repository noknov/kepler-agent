package observabilitysvc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/infra/telemetry"
	"github.com/noknov/kepler-agent/packages/observability"
)

type WorkerSignals struct {
	Ready   bool                    `json:"ready"`
	State   string                  `json:"state"`
	Metrics *observability.Snapshot `json:"metrics,omitempty"`
}

func (s *Service) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.handleHealthDashboard(w, r)
}

func (s *Service) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	if !s.authorize(r) {
		s.writeHTTPError(w, r, http.StatusForbidden, "forbidden", nil)
		return
	}
	window := 24 * time.Hour
	if raw := r.URL.Query().Get("window"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 || parsed > 7*24*time.Hour {
			s.writeHTTPError(w, r, http.StatusBadRequest, "window must be positive and no longer than 168h", nil)
			return
		}
		window = parsed
	}
	if s.overview == nil {
		s.writeHTTPError(w, r, http.StatusServiceUnavailable, "run diagnostics unavailable", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	end := time.Now().UTC()
	result, err := s.overview(ctx, end.Add(-window), end)
	if err != nil {
		s.writeHTTPError(w, r, http.StatusInternalServerError, "failed to read diagnostics", err)
		return
	}
	worker := s.workerSignals(ctx)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Overview
		Worker  WorkerSignals    `json:"worker"`
		Tracing telemetry.Status `json:"observability_tracing"`
	}{result, worker, telemetry.CurrentStatus()})
}

func (s *Service) workerSignals(ctx context.Context) WorkerSignals {
	if s.cfg.Observing.WorkerURL == "" {
		return WorkerSignals{State: "not_configured"}
	}
	base, err := url.Parse(s.cfg.Observing.WorkerURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return WorkerSignals{State: "invalid_worker_url"}
	}
	client := s.httpClient
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	// Never follow redirects from a monitoring probe to a login page or another
	// host. The target comes solely from operator config, not a query parameter.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	fetch := func(path string) ([]byte, int, error) {
		target := *base
		target.Path = strings.TrimRight(base.Path, "/") + path
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return nil, 0, err
		}
		response, err := copyClient.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if len(body) > 1<<20 {
			return nil, response.StatusCode, io.ErrShortBuffer
		}
		return body, response.StatusCode, err
	}
	_, code, err := fetch("/readyz")
	if err != nil {
		return WorkerSignals{State: "unreachable"}
	}
	signal := WorkerSignals{Ready: code == http.StatusOK, State: "available"}
	data, code, err := fetch("/metrics")
	var snapshot observability.Snapshot
	if err != nil || code != http.StatusOK || json.Unmarshal(data, &snapshot) != nil {
		signal.State = "metrics_unavailable"
		return signal
	}
	signal.Metrics = &snapshot
	return signal
}

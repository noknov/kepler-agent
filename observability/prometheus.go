package observabilitysvc

import (
	"context"
	"net/http"
	"time"

	"github.com/noknov/kepler-agent/packages/sessioninput"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func (s *Service) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorize(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.queues == nil {
		s.writeHTTPError(w, r, http.StatusServiceUnavailable, "queue diagnostics unavailable", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	snapshot, err := s.queues(ctx, time.Now().UTC())
	if err != nil {
		s.writeHTTPError(w, r, http.StatusServiceUnavailable, "queue diagnostics unavailable", err)
		return
	}
	// Per-scrape gauges describe one read-only database snapshot. They are not
	// cumulative counters and must not be summed across replicated observers.
	registry := prometheus.NewRegistry()
	inbox := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "kepler_agent_durable_inbox_events", Help: "Current durable inbox rows by state."}, []string{"state"})
	for _, state := range []string{"queued", "processing", "completed", "dead_letter"} {
		inbox.WithLabelValues(state).Set(float64(snapshot.Inbox.Statuses[state]))
	}
	for state, count := range snapshot.Inbox.Statuses {
		inbox.WithLabelValues(state).Set(float64(count))
	}
	expired := prometheus.NewGauge(prometheus.GaugeOpts{Name: "kepler_agent_durable_inbox_expired_claims", Help: "Currently expired processing claims in the durable inbox."})
	expired.Set(float64(snapshot.Inbox.ExpiredClaims))
	age := prometheus.NewGauge(prometheus.GaugeOpts{Name: "kepler_agent_durable_inbox_oldest_queued_seconds", Help: "Age of the oldest currently queued durable inbox event."})
	age.Set(snapshot.Inbox.OldestQueuedMS / 1000)
	inputs := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "kepler_agent_durable_pending_inputs", Help: "Unacknowledged session inputs by kind and claim state."}, []string{"queue"})
	for _, kind := range []sessioninput.Kind{sessioninput.KindSteering, sessioninput.KindQueue, sessioninput.KindStop, sessioninput.KindWeb} {
		for _, state := range []string{"queued", "claimed", "expired"} {
			key := string(kind) + ":" + state
			inputs.WithLabelValues(key).Set(float64(snapshot.Inputs[key]))
		}
	}
	for queue, count := range snapshot.Inputs {
		inputs.WithLabelValues(queue).Set(float64(count))
	}
	registry.MustRegister(inbox, expired, age, inputs)
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{EnableOpenMetrics: true}).ServeHTTP(w, r)
}

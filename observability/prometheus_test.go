package observabilitysvc

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/config"
	"github.com/prometheus/common/expfmt"
	prommodel "github.com/prometheus/common/model"
)

func TestDurablePrometheusAuthorizationAndFailureSemantics(t *testing.T) {
	calls := 0
	fail := false
	service := &Service{cfg: config.Config{Observing: config.ObservingConfig{AdminToken: "test-token"}}, queues: func(ctx context.Context, _ time.Time) (QueueSnapshot, error) {
		calls++
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 2*time.Second {
			t.Error("scrape has no bounded database deadline")
		}
		if fail {
			return QueueSnapshot{}, errors.New("PRIVATE_DATABASE_ERROR")
		}
		return QueueSnapshot{Inbox: QueueStats{Statuses: map[string]int64{"queued": 7}, ExpiredClaims: 2, OldestQueuedMS: 3500}, Inputs: map[string]int64{"web:expired": 4}}, nil
	}}
	unauthorized := httptest.NewRecorder()
	service.handlePrometheus(unauthorized, httptest.NewRequest("GET", "/metrics/prometheus", nil))
	if unauthorized.Code != 403 || calls != 0 {
		t.Fatal("unauthorized scrape reached durable store")
	}
	request := httptest.NewRequest("GET", "/metrics/prometheus", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	service.handlePrometheus(response, request)
	parser := expfmt.NewTextParser(prommodel.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(response.Body.String()))
	if err != nil || response.Code != 200 {
		t.Fatalf("code=%d error=%v body=%s", response.Code, err, response.Body.String())
	}
	if families["kepler_agent_durable_inbox_oldest_queued_seconds"].Metric[0].GetGauge().GetValue() != 3.5 {
		t.Fatal("queue age unit conversion failed")
	}
	if len(families["kepler_agent_durable_pending_inputs"].Metric) != 12 {
		t.Fatal("missing zero-valued queue states")
	}
	fail = true
	response = httptest.NewRecorder()
	service.handlePrometheus(response, request)
	if response.Code != 503 || strings.Contains(response.Body.String(), "PRIVATE_") || strings.Contains(response.Body.String(), "kepler_agent_durable_") {
		t.Fatal("database failure became a green/partial scrape or exposed diagnostics")
	}
}

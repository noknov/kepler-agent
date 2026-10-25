package observability

import (
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/llm"
	"github.com/prometheus/common/expfmt"
	prommodel "github.com/prometheus/common/model"
)

func TestPrometheusExposesCountersAndAggregatableDurationsWithoutContent(t *testing.T) {
	recorder := NewRecorder()
	recorder.Request()
	recorder.Denied()
	recorder.Latency(time.Second)
	recorder.LLMCall(llm.Usage{PromptTokens: 10, CompletionTokens: 4}, 2*time.Second, errors.New("PRIVATE_ERROR_CONTENT"))
	recorder.ToolCall("read", 3*time.Second, nil)
	recorder.EventInboxQueue(3, 8)
	recorder.EventInboxJob("completed")
	recorder.Event("turn_completed", map[string]any{"user": "PRIVATE_IDENTITY"})
	response := httptest.NewRecorder()
	recorder.PrometheusHandler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics/prometheus", nil))
	if response.Code != 200 || !strings.Contains(response.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("status=%d content-type=%s", response.Code, response.Header().Get("Content-Type"))
	}
	if strings.Contains(response.Body.String(), "PRIVATE_") {
		t.Fatal("private diagnostic content was exported")
	}
	parser := expfmt.NewTextParser(prommodel.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(response.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	if families["kepler_agent_model_calls_total"].Metric[0].GetCounter().GetValue() != 1 || families["kepler_agent_model_errors_total"].Metric[0].GetCounter().GetValue() != 1 {
		t.Fatal("model counter contract lost")
	}
	if families["kepler_agent_queue_depth"].Metric[0].GetGauge().GetValue() != 3 {
		t.Fatal("queue depth is not a gauge")
	}
	durations := families["kepler_agent_operation_duration_seconds"]
	if durations == nil || len(durations.Metric) != 3 {
		t.Fatal("missing operation histograms")
	}
	var totalCount uint64
	var totalSeconds float64
	for _, metric := range durations.Metric {
		totalCount += metric.GetHistogram().GetSampleCount()
		totalSeconds += metric.GetHistogram().GetSampleSum()
	}
	if totalCount != 3 || totalSeconds != 6 {
		t.Fatalf("histogram accounting: count=%d sum=%v", totalCount, totalSeconds)
	}
	other := NewRecorder()
	otherFamilies, err := other.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range otherFamilies {
		if family.GetName() == "kepler_agent_requests_total" && family.Metric[0].GetCounter().GetValue() != 0 {
			t.Fatal("independent recorder shared process-global counters")
		}
	}
}

func TestLatencyWindowRetainsRecentSamplesAndSnapshotIsolation(t *testing.T) {
	recorder := NewRecorder()
	for i := 0; i < latencySampleLimit; i++ {
		recorder.Latency(100 * time.Millisecond)
	}
	before := recorder.Snapshot()
	for i := 0; i < latencySampleLimit; i++ {
		recorder.Latency(time.Millisecond)
	}
	after := recorder.Snapshot()
	if before.LatencyMS.P95 != 100 || after.LatencyMS.P95 != 1 || after.LatencyMS.Count != 2*latencySampleLimit || after.LatencyMS.Max != 100 {
		t.Fatalf("before=%#v after=%#v", before.LatencyMS, after.LatencyMS)
	}
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		for i := 0; i < 10000; i++ {
			recorder.Latency(time.Millisecond)
		}
	}()
	for i := 0; i < 20; i++ {
		recorder.Snapshot()
	}
	group.Wait()
	if before.LatencyMS.P95 != 100 {
		t.Fatal("snapshot retained mutable sampling state")
	}
}

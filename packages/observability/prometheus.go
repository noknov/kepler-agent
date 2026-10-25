package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var recorderDescriptors = map[string]*prometheus.Desc{
	"requests":     prometheus.NewDesc("kepler_agent_requests_total", "Requests recorded by this process.", nil, nil),
	"denied":       prometheus.NewDesc("kepler_agent_denied_requests_total", "Requests denied by this process.", nil, nil),
	"models":       prometheus.NewDesc("kepler_agent_model_calls_total", "Model attempts recorded by this process.", nil, nil),
	"model_errors": prometheus.NewDesc("kepler_agent_model_errors_total", "Failed model attempts recorded by this process.", nil, nil),
	"tools":        prometheus.NewDesc("kepler_agent_tool_calls_total", "Tool calls recorded by this process.", []string{"tool"}, nil),
	"tool_errors":  prometheus.NewDesc("kepler_agent_tool_errors_total", "Failed tool calls recorded by this process.", []string{"tool"}, nil),
	"tokens":       prometheus.NewDesc("kepler_agent_tokens_total", "Reported token usage; cache/reasoning fields may overlap input/output totals.", []string{"kind"}, nil),
	"events":       prometheus.NewDesc("kepler_agent_events_total", "Canonical event counters recorded by this process.", []string{"event"}, nil),
	"jobs":         prometheus.NewDesc("kepler_agent_inbox_jobs_total", "Inbox job transitions recorded by this process.", []string{"outcome"}, nil),
	"queue":        prometheus.NewDesc("kepler_agent_queue_depth", "Current in-process inbox queue depth, not durable database backlog.", nil, nil),
	"capacity":     prometheus.NewDesc("kepler_agent_queue_capacity", "Configured in-process inbox queue capacity.", nil, nil),
	"started":      prometheus.NewDesc("kepler_agent_recorder_start_time_seconds", "Start time of this process recorder.", nil, nil),
}

type recorderCollector struct{ recorder *Recorder }

func (c recorderCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, descriptor := range recorderDescriptors {
		ch <- descriptor
	}
}
func (c recorderCollector) Collect(ch chan<- prometheus.Metric) {
	snapshot := c.recorder.Snapshot()
	emit := func(name string, kind prometheus.ValueType, value float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(recorderDescriptors[name], kind, value, labels...)
	}
	emit("requests", prometheus.CounterValue, float64(snapshot.Requests))
	emit("denied", prometheus.CounterValue, float64(snapshot.DeniedRequests))
	emit("models", prometheus.CounterValue, float64(snapshot.LLMCalls))
	emit("model_errors", prometheus.CounterValue, float64(snapshot.LLMErrors))
	for name, value := range snapshot.ToolCalls {
		emit("tools", prometheus.CounterValue, float64(value), name)
	}
	for name, value := range snapshot.ToolErrors {
		emit("tool_errors", prometheus.CounterValue, float64(value), name)
	}
	for name, value := range snapshot.AgentEvents {
		emit("events", prometheus.CounterValue, float64(value), name)
	}
	for name, value := range snapshot.EventInbox.Jobs {
		emit("jobs", prometheus.CounterValue, float64(value), name)
	}
	for name, value := range map[string]int64{"input": snapshot.LLMUsage.PromptTokens, "output": snapshot.LLMUsage.CompletionTokens, "cache_read": snapshot.LLMUsage.CacheReadInputTokens, "cache_create": snapshot.LLMUsage.CacheCreationInputTokens, "reasoning": snapshot.LLMUsage.ReasoningTokens} {
		emit("tokens", prometheus.CounterValue, float64(value), name)
	}
	emit("queue", prometheus.GaugeValue, float64(snapshot.EventInbox.QueueDepth))
	emit("capacity", prometheus.GaugeValue, float64(snapshot.EventInbox.QueueCapacity))
	emit("started", prometheus.GaugeValue, float64(snapshot.StartedAt.UnixNano())/1e9)
}

func initPrometheus(r *Recorder) {
	// A private registry permits multiple independent recorders and avoids global
	// collector collisions in tests or embedded deployments. Existing counters
	// remain authoritative; only aggregatable duration distributions are new.
	r.registry = prometheus.NewRegistry()
	durations := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "kepler_agent", Name: "operation_duration_seconds", Help: "Duration of recorded request/model/tool operations, in seconds.",
		Buckets:                     []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300, 900, 1800},
		NativeHistogramBucketFactor: 1.1, NativeHistogramMaxBucketNumber: 160,
	}, []string{"operation"})
	r.durationObservers = [3]prometheus.Observer{durations.WithLabelValues("request"), durations.WithLabelValues("model"), durations.WithLabelValues("tool")}
	r.registry.MustRegister(recorderCollector{r}, durations, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

// PrometheusHandler exposes standard metrics separately from the diagnostics
// JSON. It exports neither error messages nor identity/content labels.
func (r *Recorder) PrometheusHandler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

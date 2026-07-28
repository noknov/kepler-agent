package observability

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/llm"
)

type Snapshot struct {
	StartedAt        time.Time        `json:"started_at"`
	Requests         int64            `json:"requests"`
	DeniedRequests   int64            `json:"denied_requests"`
	LLMCalls         int64            `json:"llm_calls"`
	LLMErrors        int64            `json:"llm_errors"`
	LLMUsage         TokenUsage       `json:"llm_usage"`
	EstimatedCostUSD float64          `json:"estimated_cost_usd,omitempty"`
	ToolCalls        map[string]int64 `json:"tool_calls"`
	ToolErrors       map[string]int64 `json:"tool_errors"`
	AgentEvents      map[string]int64 `json:"agent_events,omitempty"`
	ReactionFeedback map[string]int64 `json:"reaction_feedback"`
	LatencyMS        LatencySummary   `json:"latency_ms"`
	LLMLatencyMS     LatencySummary   `json:"llm_latency_ms"`
	ToolLatencyMS    LatencySummary   `json:"tool_latency_ms"`
	LastErrors       []string         `json:"last_errors,omitempty"`
}

type LatencySummary struct {
	Count int64 `json:"count"`
	Avg   int64 `json:"avg"`
	Max   int64 `json:"max"`
}

type TokenUsage struct {
	PromptTokens             int64 `json:"prompt_tokens"`
	CompletionTokens         int64 `json:"completion_tokens"`
	TotalTokens              int64 `json:"total_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	ReasoningTokens          int64 `json:"reasoning_tokens,omitempty"`
}

type Recorder struct {
	mu         sync.Mutex
	startedAt  time.Time
	snap       Snapshot
	latSum     int64
	llmLatSum  int64
	toolLatSum int64
	rates      CostRates
}

func NewRecorder() *Recorder {
	now := time.Now().UTC()
	return &Recorder{
		startedAt: now,
		snap: Snapshot{
			StartedAt:        now,
			ToolCalls:        map[string]int64{},
			ToolErrors:       map[string]int64{},
			AgentEvents:      map[string]int64{},
			ReactionFeedback: map[string]int64{},
		},
	}
}

func (r *Recorder) SetCostRates(rates CostRates) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rates = rates
}

func (r *Recorder) Request() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.Requests++
}

func (r *Recorder) Denied() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.DeniedRequests++
}

func (r *Recorder) LLMCall(usage llm.Usage, d time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.LLMCalls++
	r.snap.LLMUsage.PromptTokens += int64(usage.PromptTokens)
	r.snap.LLMUsage.CompletionTokens += int64(usage.CompletionTokens)
	r.snap.LLMUsage.TotalTokens += int64(usage.TotalTokens)
	r.snap.LLMUsage.CacheReadInputTokens += int64(usage.CacheReadInputTokens)
	r.snap.LLMUsage.CacheCreationInputTokens += int64(usage.CacheCreationInputTokens)
	r.snap.LLMUsage.ReasoningTokens += int64(usage.ReasoningTokens)
	r.snap.EstimatedCostUSD += r.rates.EstimateUSD(usage)
	r.addLatencyLocked(&r.snap.LLMLatencyMS, &r.llmLatSum, d)
	if err != nil {
		r.snap.LLMErrors++
		r.addErrorLocked("llm: " + err.Error())
	}
}

func (r *Recorder) ToolCall(name string, d time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.ToolCalls[name]++
	r.addLatencyLocked(&r.snap.ToolLatencyMS, &r.toolLatSum, d)
	if err != nil {
		r.snap.ToolErrors[name]++
		r.addErrorLocked(name + ": " + err.Error())
	}
}

func (r *Recorder) Event(name string, metadata map[string]any) {
	if name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snap.AgentEvents == nil {
		r.snap.AgentEvents = map[string]int64{}
	}
	r.snap.AgentEvents[name]++
	if errText, ok := metadata["error"].(string); ok && errText != "" {
		r.addErrorLocked(name + ": " + errText)
	}
}

func (r *Recorder) Reaction(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.ReactionFeedback[name]++
}

func (r *Recorder) Latency(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addLatencyLocked(&r.snap.LatencyMS, &r.latSum, d)
}

func (r *Recorder) addLatencyLocked(summary *LatencySummary, sum *int64, d time.Duration) {
	ms := d.Milliseconds()
	summary.Count++
	*sum += ms
	summary.Avg = *sum / summary.Count
	if ms > summary.Max {
		summary.Max = ms
	}
}

func (r *Recorder) Error(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addErrorLocked(err.Error())
}

func (r *Recorder) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := r.snap
	cp.ToolCalls = copyMap(r.snap.ToolCalls)
	cp.ToolErrors = copyMap(r.snap.ToolErrors)
	cp.AgentEvents = copyMap(r.snap.AgentEvents)
	cp.ReactionFeedback = copyMap(r.snap.ReactionFeedback)
	cp.LastErrors = append([]string(nil), r.snap.LastErrors...)
	sort.Strings(cp.LastErrors)
	return cp
}

func (r *Recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(r.Snapshot())
}

func (r *Recorder) addErrorLocked(msg string) {
	log.Printf("observability error: %s", msg)
	r.snap.LastErrors = append(r.snap.LastErrors, msg)
	if len(r.snap.LastErrors) > 20 {
		r.snap.LastErrors = r.snap.LastErrors[len(r.snap.LastErrors)-20:]
	}
}

func copyMap(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

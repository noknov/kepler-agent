package observability

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

type Snapshot struct {
	StartedAt        time.Time        `json:"started_at"`
	Requests         int64            `json:"requests"`
	DeniedRequests   int64            `json:"denied_requests"`
	LLMCalls         int64            `json:"llm_calls"`
	ToolCalls        map[string]int64 `json:"tool_calls"`
	ToolErrors       map[string]int64 `json:"tool_errors"`
	ReactionFeedback map[string]int64 `json:"reaction_feedback"`
	LatencyMS        LatencySummary   `json:"latency_ms"`
	LastErrors       []string         `json:"last_errors,omitempty"`
}

type LatencySummary struct {
	Count int64 `json:"count"`
	Avg   int64 `json:"avg"`
	Max   int64 `json:"max"`
}

type Recorder struct {
	mu        sync.Mutex
	startedAt time.Time
	snap      Snapshot
	latSum    int64
}

func NewRecorder() *Recorder {
	now := time.Now().UTC()
	return &Recorder{
		startedAt: now,
		snap: Snapshot{
			StartedAt:        now,
			ToolCalls:        map[string]int64{},
			ToolErrors:       map[string]int64{},
			ReactionFeedback: map[string]int64{},
		},
	}
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

func (r *Recorder) LLMCall() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.LLMCalls++
}

func (r *Recorder) ToolCall(name string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.ToolCalls[name]++
	if err != nil {
		r.snap.ToolErrors[name]++
		r.addErrorLocked(name + ": " + err.Error())
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
	ms := d.Milliseconds()
	r.snap.LatencyMS.Count++
	r.latSum += ms
	r.snap.LatencyMS.Avg = r.latSum / r.snap.LatencyMS.Count
	if ms > r.snap.LatencyMS.Max {
		r.snap.LatencyMS.Max = ms
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

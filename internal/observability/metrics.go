package observability

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
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
	ReactionFeedback map[string]int64 `json:"reaction_feedback"`
	LatencyMS        LatencySummary   `json:"latency_ms"`
	LLMLatencyMS     LatencySummary   `json:"llm_latency_ms"`
	ToolLatencyMS    LatencySummary   `json:"tool_latency_ms"`
	RAG              RAGSnapshot      `json:"rag,omitempty"`
	LastErrors       []string         `json:"last_errors,omitempty"`
}

type RAGSnapshot struct {
	IndexRuns       int64                    `json:"index_runs,omitempty"`
	IndexErrors     int64                    `json:"index_errors,omitempty"`
	Searches        int64                    `json:"searches,omitempty"`
	SearchErrors    int64                    `json:"search_errors,omitempty"`
	SearchResults   int64                    `json:"search_results,omitempty"`
	SearchStaleHits int64                    `json:"search_stale_hits,omitempty"`
	Indexes         map[string]RAGIndexState `json:"indexes,omitempty"`
}

type RAGIndexState struct {
	Repo                   string    `json:"repo"`
	Branch                 string    `json:"branch"`
	LastCommit             string    `json:"last_commit,omitempty"`
	LastIndexedAt          time.Time `json:"last_indexed_at,omitempty"`
	LastDurationMS         int64     `json:"last_duration_ms,omitempty"`
	LastFilesChanged       int       `json:"last_files_changed,omitempty"`
	LastChunksAdded        int       `json:"last_chunks_added,omitempty"`
	LastChunksReused       int       `json:"last_chunks_reused,omitempty"`
	LastChunksSplitLarge   int       `json:"last_chunks_split_large,omitempty"`
	LastChunksSkippedLarge int       `json:"last_chunks_skipped_large,omitempty"`
	LastError              string    `json:"last_error,omitempty"`
	Runs                   int64     `json:"runs,omitempty"`
	Failures               int64     `json:"failures,omitempty"`
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
			ReactionFeedback: map[string]int64{},
			RAG: RAGSnapshot{
				Indexes: map[string]RAGIndexState{},
			},
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

func (r *Recorder) RAGIndexSuccess(repo, branch, commit string, filesChanged, chunksAdded, chunksReused, chunksSplitLarge, chunksSkippedLarge int, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureRAGLocked()
	r.snap.RAG.IndexRuns++
	key := ragIndexKey(repo, branch)
	state := r.snap.RAG.Indexes[key]
	state.Repo = repo
	state.Branch = branch
	state.LastCommit = commit
	state.LastIndexedAt = time.Now().UTC()
	state.LastDurationMS = d.Milliseconds()
	state.LastFilesChanged = filesChanged
	state.LastChunksAdded = chunksAdded
	state.LastChunksReused = chunksReused
	state.LastChunksSplitLarge = chunksSplitLarge
	state.LastChunksSkippedLarge = chunksSkippedLarge
	state.LastError = ""
	state.Runs++
	r.snap.RAG.Indexes[key] = state
}

func (r *Recorder) RAGIndexError(repo, branch string, d time.Duration, err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureRAGLocked()
	r.snap.RAG.IndexRuns++
	r.snap.RAG.IndexErrors++
	key := ragIndexKey(repo, branch)
	state := r.snap.RAG.Indexes[key]
	state.Repo = repo
	state.Branch = branch
	state.LastIndexedAt = time.Now().UTC()
	state.LastDurationMS = d.Milliseconds()
	state.LastError = err.Error()
	state.Runs++
	state.Failures++
	r.snap.RAG.Indexes[key] = state
	r.addErrorLocked("rag/index " + key + ": " + err.Error())
}

func (r *Recorder) RAGSearch(results int, stale bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureRAGLocked()
	r.snap.RAG.Searches++
	if err != nil {
		r.snap.RAG.SearchErrors++
		r.addErrorLocked("rag/search: " + err.Error())
		return
	}
	r.snap.RAG.SearchResults += int64(results)
	if stale {
		r.snap.RAG.SearchStaleHits++
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
	cp.ReactionFeedback = copyMap(r.snap.ReactionFeedback)
	cp.RAG.Indexes = copyRAGIndexes(r.snap.RAG.Indexes)
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

func (r *Recorder) ensureRAGLocked() {
	if r.snap.RAG.Indexes == nil {
		r.snap.RAG.Indexes = map[string]RAGIndexState{}
	}
}

func copyMap(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyRAGIndexes(in map[string]RAGIndexState) map[string]RAGIndexState {
	out := make(map[string]RAGIndexState, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func ragIndexKey(repo, branch string) string {
	return repo + "@" + branch
}

package runs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/observability"
)

type Observer struct {
	Store              Store
	Run                *Run
	Rates              observability.CostRates
	mu                 sync.Mutex
	stepSeq            int
	started            time.Time
	finished           bool
	persistErr         error
	OnPersistenceError func(error)
}

func NewObserver(store Store, run Run, rates observability.CostRates) *Observer {
	now := time.Now().UTC()
	if run.ID == "" {
		run.ID = NewID()
	}
	if run.TraceID == "" {
		run.TraceID = NewTraceID()
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	if run.Status == "" {
		run.Status = "running"
	}
	o := &Observer{Store: store, Run: &run, Rates: rates, started: run.StartedAt}
	o.saveWithTimeout()
	return o
}

func (o *Observer) LLMCall(usage llm.Usage, d time.Duration, err error) {
	o.recordLLMStep(usage, d, err, Step{})
}

func (o *Observer) LLMResponse(resp llm.Response, d time.Duration, err error) {
	step := Step{
		FinishReason:  resp.FinishReason,
		Content:       resp.Message.Content,
		ToolCallNames: toolCallNames(resp.Message.ToolCalls),
	}
	o.recordLLMStep(resp.Usage, d, err, step)
}

func (o *Observer) recordLLMStep(usage llm.Usage, d time.Duration, err error, step Step) {
	o.mu.Lock()
	defer o.mu.Unlock()
	cost := o.Rates.EstimateUSD(usage)
	o.Run.Usage.PromptTokens += usage.PromptTokens
	o.Run.Usage.CompletionTokens += usage.CompletionTokens
	o.Run.Usage.TotalTokens += usage.TotalTokens
	o.Run.Usage.CacheReadInputTokens += usage.CacheReadInputTokens
	o.Run.Usage.CacheCreationInputTokens += usage.CacheCreationInputTokens
	o.Run.Usage.ReasoningTokens += usage.ReasoningTokens
	// Propagate the provider-style flag: if any call uses OpenAI-style semantics
	// (cache already included in PromptTokens), mark the accumulated usage accordingly
	// so consumers can compute total billed tokens correctly.
	if usage.CacheIncludedInPrompt {
		o.Run.Usage.CacheIncludedInPrompt = true
	}
	o.Run.EstimatedCostUSD += cost
	step.Type = "llm"
	step.Name = o.Run.Model
	step.DurationMS = d.Milliseconds()
	step.Usage = usage
	step.EstimatedCostUSD = cost
	step.Error = errorString(err)
	o.appendStepLocked(step)
}

func (o *Observer) ToolCall(name string, d time.Duration, err error) {
	o.ToolCallWithMetadata(name, nil, d, err)
}

func (o *Observer) ToolCallWithMetadata(name string, args json.RawMessage, d time.Duration, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.appendStepLocked(Step{
		Type:       "tool",
		Name:       name,
		DurationMS: d.Milliseconds(),
		Error:      errorString(err),
		Metadata:   toolMetadata(args),
	})
}

func (o *Observer) Event(name string, metadata map[string]any) {
	if o == nil || name == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.appendStepLocked(Step{
		Type:     "event",
		Name:     name,
		Metadata: metadata,
	})
}

func (o *Observer) RecordErrorStack(stack string) {
	if o == nil || o.Run == nil || stack == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Run.ErrorStack = stack
	o.saveLockedWithTimeout()
}

func (o *Observer) Finish(status, errorID string, err error, final string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return
	}
	o.finished = true
	o.Run.Status = status
	o.Run.ErrorID = errorID
	o.Run.Error = errorString(err)
	if final != "" {
		o.Run.FinalHash = HashText(final)
	}
	o.Run.EndedAt = time.Now().UTC()
	o.Run.DurationMS = o.Run.EndedAt.Sub(o.Run.StartedAt).Milliseconds()
	o.Run.Quality = scoreRun(*o.Run)
	o.saveLockedWithTimeout()
}

// BilledTokens returns the total number of tokens billed across all completed
// LLM calls in this run.  It is safe to call concurrently with ongoing calls.
// The semantics are provider-aware: for OpenAI-compatible APIs, cache tokens
// are already included in PromptTokens; for Anthropic they are independent.
func (o *Observer) BilledTokens() int {
	if o == nil || o.Run == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return BilledTokens(o.Run.Usage)
}

// BilledTokens returns the provider-aware billed token count for accumulated
// usage. OpenAI-compatible cache tokens are included in PromptTokens; Anthropic
// cache tokens are independent.
func BilledTokens(usage llm.Usage) int {
	if usage.CacheIncludedInPrompt {
		return usage.PromptTokens + usage.CompletionTokens
	}
	return usage.PromptTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens + usage.CompletionTokens
}

func (o *Observer) LinkSlackMessage(channel, messageTS string) {
	if o == nil || o.Run == nil || messageTS == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Run.SlackChannel = channel
	o.Run.SlackMessageTS = messageTS
	o.saveLockedWithTimeout()
}

func (o *Observer) appendStepLocked(step Step) {
	o.stepSeq++
	step.ID = o.Run.ID + "-step-" + time.Now().UTC().Format("150405.000000000")
	step.SpanID = NewSpanID()
	step.ParentSpanID = o.Run.TraceID
	step.StartedAt = time.Now().UTC().Add(-time.Duration(step.DurationMS) * time.Millisecond)
	o.Run.Steps = append(o.Run.Steps, step)
	if store, ok := o.Store.(StepStore); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		o.recordPersistenceErrorLocked(store.AppendStep(ctx, o.Run.ID, step))
		cancel()
	} else {
		o.saveLockedWithTimeout()
	}
}

func toolCallNames(calls []llm.ToolCall) []string {
	if len(calls) == 0 {
		return nil
	}
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		if call.Function.Name != "" {
			names = append(names, call.Function.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

func (o *Observer) save(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.saveLocked(ctx)
}

func (o *Observer) saveWithTimeout() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o.save(ctx)
}

func (o *Observer) saveLockedWithTimeout() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o.saveLocked(ctx)
}

func (o *Observer) saveLocked(ctx context.Context) {
	if o.Store == nil || o.Run == nil {
		return
	}
	o.recordPersistenceErrorLocked(o.Store.Save(ctx, *o.Run))
}

func (o *Observer) PersistenceError() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.persistErr
}

func (o *Observer) recordPersistenceErrorLocked(err error) {
	if err == nil {
		return
	}
	o.persistErr = errors.Join(o.persistErr, err)
	if o.OnPersistenceError != nil {
		o.OnPersistenceError(err)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func toolMetadata(args json.RawMessage) map[string]any {
	if len(args) == 0 {
		return nil
	}
	compact := make([]byte, 0, len(args))
	var buf bytes.Buffer
	if json.Compact(&buf, args) == nil {
		compact = buf.Bytes()
	} else {
		compact = []byte(args)
	}
	sum := sha256.Sum256(compact)
	meta := map[string]any{
		"args_hash":  "sha256:" + hex.EncodeToString(sum[:8]),
		"args_bytes": len(compact),
	}
	var object map[string]any
	if json.Unmarshal(compact, &object) == nil {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		meta["args_keys"] = keys
	}
	return meta
}

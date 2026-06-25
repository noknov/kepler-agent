package agent

import (
	"context"
	"sync"

	"github.com/wati/oncall-agent/internal/llm"
)

const maxStreamingConcurrency = 10

// streamingToolExecutor starts executing tool calls as they complete during
// LLM streaming, rather than waiting for the full response. This matches
// Claude Code's StreamingToolExecutor pattern and dramatically reduces
// perceived latency for multi-tool responses.
type streamingToolExecutor struct {
	ctx             context.Context
	runner          Runner
	req             Request
	seenToolCalls   map[string]int
	seenSearchTerms map[string]int

	mu      sync.Mutex
	results map[string]toolResult
	wg      sync.WaitGroup
	sem     chan struct{}
}

func newStreamingToolExecutor(ctx context.Context, r Runner, req Request, seenToolCalls, seenSearchTerms map[string]int) *streamingToolExecutor {
	return &streamingToolExecutor{
		ctx:             ctx,
		runner:          r,
		req:             req,
		seenToolCalls:   seenToolCalls,
		seenSearchTerms: seenSearchTerms,
		results:         make(map[string]toolResult),
		sem:             make(chan struct{}, maxStreamingConcurrency),
	}
}

// Submit starts executing a tool call that completed during streaming.
// Only concurrency-safe (read-only) tools are executed eagerly;
// write tools are deferred to maintain correctness.
func (e *streamingToolExecutor) Submit(call llm.ToolCall) {
	name := call.Function.Name
	if e.runner.Tools == nil {
		return
	}
	if !e.runner.Tools.CanRunInParallel(name) {
		return
	}

	e.mu.Lock()
	if dup, content := e.runner.duplicateToolCall(call, e.seenToolCalls, e.seenSearchTerms); dup {
		e.results[call.ID] = toolResult{
			message: llm.Message{
				Role: "tool", ToolCallID: call.ID, Name: name,
				Content: content,
			},
			name: name,
			err:  ErrRepeatedToolCall,
		}
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.sem <- struct{}{}
		defer func() { <-e.sem }()

		result := e.runner.executeSingleTool(e.ctx, call, e.req, false)
		e.mu.Lock()
		e.results[call.ID] = result
		e.mu.Unlock()
	}()
}

// HasResults reports whether any tool calls were executed during streaming.
func (e *streamingToolExecutor) HasResults() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	n := len(e.results)
	e.mu.Unlock()
	return n > 0
}

// Drain waits for all in-flight executions and returns results ordered to
// match the final tool call list. Tools not pre-executed during streaming
// are executed synchronously here.
func (e *streamingToolExecutor) Drain(calls []llm.ToolCall) []toolResult {
	e.wg.Wait()

	results := make([]toolResult, len(calls))
	e.mu.Lock()
	preExecuted := e.results
	e.mu.Unlock()

	var remaining []int
	for i, call := range calls {
		if result, ok := preExecuted[call.ID]; ok {
			results[i] = result
		} else {
			remaining = append(remaining, i)
		}
	}

	// Execute tools that weren't pre-executed (non-parallel or submitted late)
	for _, i := range remaining {
		call := calls[i]
		if dup, content := e.runner.duplicateToolCall(call, e.seenToolCalls, e.seenSearchTerms); dup {
			results[i] = toolResult{
				message: llm.Message{
					Role: "tool", ToolCallID: call.ID, Name: call.Function.Name,
					Content: content,
				},
				name: call.Function.Name,
				err:  ErrRepeatedToolCall,
			}
			continue
		}
		results[i] = e.runner.executeSingleTool(e.ctx, call, e.req, true)
	}

	e.runner.observeToolResults(results)
	return results
}


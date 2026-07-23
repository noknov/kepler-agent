package agent

import (
	"context"
	"sync"

	"github.com/noknov/slack-copilot-agent/internal/llm"
)

const maxStreamingConcurrency = 10

// streamingToolExecutor starts executing tool calls as they complete during
// LLM streaming, rather than waiting for the full response. This matches
// Claude Code's StreamingToolExecutor pattern and dramatically reduces
// perceived latency for multi-tool responses.
type streamingToolExecutor struct {
	ctx    context.Context
	runner Runner
	req    Request

	mu      sync.Mutex
	results map[string]toolResult
	started map[string]bool
	// Once a non-parallel tool appears in the streamed call list, later calls
	// must wait for Drain so execution order matches the model's intent.
	serialBarrier bool
	wg            sync.WaitGroup
	sem           chan struct{}
}

func newStreamingToolExecutor(ctx context.Context, r Runner, req Request) *streamingToolExecutor {
	return &streamingToolExecutor{
		ctx:     ctx,
		runner:  r,
		req:     req,
		results: make(map[string]toolResult),
		started: make(map[string]bool),
		sem:     make(chan struct{}, maxStreamingConcurrency),
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
		e.mu.Lock()
		e.serialBarrier = true
		e.mu.Unlock()
		return
	}

	e.mu.Lock()
	if e.serialBarrier || e.started[call.ID] {
		e.mu.Unlock()
		return
	}
	e.started[call.ID] = true
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

// HasSubmitted reports whether any tool execution began during streaming.
func (e *streamingToolExecutor) HasSubmitted() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	n := len(e.started)
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
		results[i] = e.runner.executeSingleTool(e.ctx, call, e.req, true)
	}

	e.runner.observeToolResults(results)
	return results
}

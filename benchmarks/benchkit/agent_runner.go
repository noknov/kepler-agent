package benchkit

import (
	"context"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent"
	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

type RunnerAgent struct {
	Runner       agent.Runner
	Locale       string
	SystemPrompt string
}

func (a RunnerAgent) RunCase(ctx context.Context, c Case) (AgentResult, error) {
	runner := a.Runner
	observer := &MetricsObserver{}
	runner.Observer = observer
	runner.OnLLMStepComplete = func() {
		observer.LLMStep()
	}
	systemPrompt := a.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "You are being evaluated in a software engineering benchmark. Use available tools for code evidence, be concise, and complete the requested task."
	}
	req := agent.Request{
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: c.Prompt},
		},
		UserQuestion: c.Prompt,
		Runtime: registry.Runtime{
			UserID:   "benchmark",
			Channel:  "benchmark",
			ThreadTS: c.ID,
		},
		Locale: a.Locale,
	}
	result, err := runner.Run(ctx, req)
	out := AgentResult{
		Output:            result.Final,
		TerminationReason: string(result.TerminationReason),
		LLMCalls:          observer.LLMCalls,
		ToolCalls:         observer.ToolCalls,
		ToolCallCounts:    observer.ToolCallCounts,
	}
	if err != nil {
		return out, err
	}
	return out, nil
}

type MetricsObserver struct {
	LLMCalls       int
	ToolCalls      int
	ToolCallCounts map[string]int
}

func (o *MetricsObserver) LLMCall(_ llm.Usage, _ time.Duration, _ error) {}

func (o *MetricsObserver) ToolCall(name string, _ time.Duration, _ error) {
	o.ToolCalls++
	if o.ToolCallCounts == nil {
		o.ToolCallCounts = map[string]int{}
	}
	o.ToolCallCounts[name]++
}

func (o *MetricsObserver) LLMStep() {
	o.LLMCalls++
}

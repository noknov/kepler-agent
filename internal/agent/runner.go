package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type Observer interface {
	LLMCall()
	ToolCall(name string, err error)
	Latency(d time.Duration)
}

type StatusUpdater func(status string)

type ObservationFormatter interface {
	ToolObservation(toolName string, output string) string
}

type Sanitizer interface {
	Sanitize(text string) string
}

type Runner struct {
	LLM          llm.Client
	Model        string
	Thinking     string
	MaxTokens    int
	Temp         float64
	Tools        *registry.Registry
	Format       ObservationFormatter
	Sanitize     Sanitizer
	Observer     Observer
	MaxSteps     int
	StatusUpdate StatusUpdater
}

type Request struct {
	Messages []llm.Message
	Runtime  registry.Runtime
}

type Result struct {
	Generated       []llm.Message
	Final           string
	Pending         bool
	PendingQuestion string
}

func (r Runner) Run(ctx context.Context, req Request) (Result, error) {
	maxSteps := r.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 12
	}
	messages := append([]llm.Message(nil), req.Messages...)
	var generated []llm.Message

	for step := 0; step < maxSteps; step++ {
		if r.Observer != nil {
			r.Observer.LLMCall()
		}
		if r.StatusUpdate != nil {
			if step == 0 {
				r.StatusUpdate("Analyzing...")
			} else {
				r.StatusUpdate(fmt.Sprintf("Processing... (step %d)", step+1))
			}
		}
		resp, err := r.LLM.Chat(ctx, llm.Request{
			Model:       r.Model,
			Messages:    messages,
			Tools:       r.Tools.Specs(),
			MaxTokens:   r.MaxTokens,
			Temperature: r.Temp,
			Thinking:    r.Thinking,
		})
		if err != nil {
			return Result{Generated: generated}, err
		}

		assistantMsg := resp.Message
		messages = append(messages, assistantMsg)
		generated = append(generated, assistantMsg)

		if len(assistantMsg.ToolCalls) == 0 {
			if r.StatusUpdate != nil {
				r.StatusUpdate("Generating response...")
			}
			final := strings.TrimSpace(r.sanitize(assistantMsg.Content))
			if final == "" {
				final = "I didn't get a valid response. Please try again or provide more context."
			}
			return Result{Generated: generated, Final: final}, nil
		}

		for _, call := range assistantMsg.ToolCalls {
			name := call.Function.Name
			if r.StatusUpdate != nil {
				r.StatusUpdate(toolStatusHint(name))
			}
			args := json.RawMessage(call.Function.Arguments)
			start := time.Now()
			result, err := r.Tools.Execute(ctx, name, args, req.Runtime)
			if r.Observer != nil {
				r.Observer.ToolCall(name, err)
				r.Observer.Latency(time.Since(start))
			}
			content := ""
			if err != nil {
				content = "[tool error] " + err.Error()
			} else {
				content = r.format(name, r.sanitize(result.Content))
			}
			toolMessage := llm.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Name:       name,
				Content:    content,
			}
			messages = append(messages, toolMessage)
			generated = append(generated, toolMessage)

			if err == nil && result.WaitForUser {
				return Result{
					Generated:       generated,
					Pending:         true,
					PendingQuestion: content,
				}, nil
			}
		}
	}
	return Result{Generated: generated}, fmt.Errorf("agent exceeded max tool steps")
}

func (r Runner) format(toolName, output string) string {
	if r.Format == nil {
		return output
	}
	return r.Format.ToolObservation(toolName, output)
}

func (r Runner) sanitize(text string) string {
	if r.Sanitize == nil {
		return text
	}
	return r.Sanitize.Sanitize(text)
}

var toolHints = map[string]string{
	"code-read_file":     "Reading code...",
	"code-search":        "Searching code...",
	"git-status":         "Checking repo status...",
	"git-log":            "Reading commit history...",
	"git-show":           "Inspecting commit...",
	"gcp-logs":           "Querying logs...",
	"notion-search":      "Searching docs...",
	"notion-create_page": "Creating page...",
	"youtrack-get_issue": "Fetching issue...",
	"youtrack-search":    "Searching issues...",
	"slack-ask_user":     "Asking for more info...",
	"delegate-run":       "Deep analysis...",
}

func toolStatusHint(name string) string {
	if hint, ok := toolHints[name]; ok {
		return hint
	}
	return "Processing..."
}

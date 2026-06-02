package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/prompts"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

var (
	ErrRepetitiveOutput = errors.New("model output repeated itself")
	ErrRepeatedToolCall = errors.New("model repeated the same tool call")
	ErrTextualToolCall  = errors.New("model returned textual tool invocation instead of structured tool calls")
)

type Observer interface {
	LLMCall(usage llm.Usage, d time.Duration, err error)
	ToolCall(name string, d time.Duration, err error)
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

const repetitiveRetryPrompt = "Your previous answer became repetitive. Give one concise final answer only. Do not repeat sentences. Do not narrate further investigation. If evidence is insufficient, say the next check in one short paragraph."

const textualToolCallRetryPrompt = "Your previous reply included textual tool-call markup (for example <tool_call> or <function=...>) instead of using the API's structured tool calling. Do not output tool XML or pseudo tool syntax. Either call tools through the provided tool interface, or give a concise final answer in plain language using evidence already gathered."

func (r Runner) Run(ctx context.Context, req Request) (Result, error) {
	maxSteps := r.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 12
	}
	messages := append([]llm.Message(nil), req.Messages...)
	var generated []llm.Message
	seenToolCalls := map[string]int{}
	retriedRepetitiveFinal := false
	retriedTextualToolCall := false

	for step := 0; step < maxSteps; step++ {
		lastStep := step == maxSteps-1 || retriedRepetitiveFinal || retriedTextualToolCall
		if r.StatusUpdate != nil {
			if step == 0 {
				r.StatusUpdate("Analyzing...")
			} else {
				r.StatusUpdate(fmt.Sprintf("Processing... (step %d)", step+1))
			}
		}

		// On the last step, omit tools so the model is forced to produce a
		// final text response using whatever it has gathered so far.
		var toolSpecs []llm.ToolSpec
		if !lastStep {
			toolSpecs = r.Tools.Specs()
		}

		llmStart := time.Now()
		resp, err := r.LLM.Chat(ctx, llm.Request{
			Model:       r.Model,
			Messages:    messages,
			Tools:       toolSpecs,
			MaxTokens:   r.MaxTokens,
			Temperature: r.Temp,
			Thinking:    r.Thinking,
		})
		if r.Observer != nil {
			r.Observer.LLMCall(resp.Usage, time.Since(llmStart), err)
		}
		if err != nil {
			return Result{Generated: generated}, err
		}

		assistantMsg := resp.Message
		if len(assistantMsg.ToolCalls) == 0 {
			if r.StatusUpdate != nil {
				r.StatusUpdate("Generating response...")
			}
			final := strings.TrimSpace(r.sanitize(assistantMsg.Content))
			if final == "" {
				final = "I didn't get a valid response. Please try again or provide more context."
			}
			if llm.LooksLikeTextualToolCall(final) {
				if !retriedTextualToolCall {
					retriedTextualToolCall = true
					messages = append(messages, llm.Message{Role: "system", Content: textualToolCallRetryPrompt})
					if r.StatusUpdate != nil {
						r.StatusUpdate("Retrying after invalid tool format...")
					}
					continue
				}
				return Result{Generated: generated}, ErrTextualToolCall
			}
			if looksRepetitive(final) {
				if !retriedRepetitiveFinal {
					retriedRepetitiveFinal = true
					messages = append(messages, llm.Message{Role: "system", Content: repetitiveRetryPrompt})
					if r.StatusUpdate != nil {
						r.StatusUpdate("Retrying final response...")
					}
					continue
				}
				return Result{Generated: generated}, ErrRepetitiveOutput
			}
			assistantMsg.Content = final
			messages = append(messages, assistantMsg)
			generated = append(generated, assistantMsg)
			return Result{Generated: generated, Final: final}, nil
		}

		// Text before a tool call is only a transient narration. Persisting it
		// pollutes future turns and can amplify model loops, so keep only the
		// structured tool calls in the conversation state.
		assistantMsg.Content = ""
		messages = append(messages, assistantMsg)
		generated = append(generated, assistantMsg)

		for _, call := range assistantMsg.ToolCalls {
			name := call.Function.Name
			signature := toolCallSignature(call)
			seenToolCalls[signature]++
			if seenToolCalls[signature] > 2 {
				return Result{Generated: generated}, fmt.Errorf("%w: %s", ErrRepeatedToolCall, name)
			}
			if r.StatusUpdate != nil {
				r.StatusUpdate(toolStatusHint(name))
			}
			args := json.RawMessage(call.Function.Arguments)
			start := time.Now()
			result, err := r.Tools.Execute(ctx, name, args, req.Runtime)
			if r.Observer != nil {
				r.Observer.ToolCall(name, time.Since(start), err)
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

func toolCallSignature(call llm.ToolCall) string {
	args := strings.Join(strings.Fields(call.Function.Arguments), " ")
	return call.Function.Name + "\x00" + args
}

func looksRepetitive(text string) bool {
	normalized := strings.TrimSpace(text)
	if len([]rune(normalized)) < 160 {
		return false
	}
	units := repeatedUnits(normalized)
	if len(units) < 8 {
		return false
	}
	counts := map[string]int{}
	total := 0
	for _, unit := range units {
		runeLen := len([]rune(unit))
		if runeLen < 8 {
			continue
		}
		counts[unit]++
		total++
		if counts[unit] >= 6 && counts[unit]*runeLen >= len([]rune(normalized))/3 {
			return true
		}
	}
	if total == 0 {
		return false
	}
	for _, count := range counts {
		if count >= 8 && count*2 >= total {
			return true
		}
	}
	return false
}

func repeatedUnits(text string) []string {
	splitter := regexp.MustCompile(`[。！？!?;\n]+`)
	raw := splitter.Split(text, -1)
	units := make([]string, 0, len(raw))
	for _, unit := range raw {
		unit = strings.Join(strings.Fields(strings.TrimSpace(unit)), " ")
		if unit != "" {
			units = append(units, unit)
		}
	}
	return units
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
	"code-read_file":           "Reading code...",
	"code-search":              "Searching code...",
	"git-status":               "Checking repo status...",
	"git-log":                  "Reading commit history...",
	"git-show":                 "Inspecting commit...",
	"gcp-logs":                 "Querying logs...",
	"notion-search":            "Searching docs...",
	"notion-create_page":       "Creating page...",
	"youtrack-get_issue":       "Fetching issue...",
	"youtrack-search":          "Searching issues...",
	"github-dispatch_workflow": "Triggering GitHub workflow...",
	"github-workflow_runs":     "Checking GitHub workflow runs...",
	"slack-ask_user":           "Asking for more info...",
	"delegate-run":             "Deep analysis...",
}

func toolStatusHint(name string) string {
	if hint, ok := toolHints[name]; ok {
		return prompts.ToolStatus(name, hint)
	}
	return prompts.ToolStatus("default", "Processing...")
}

package delegation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/prompts"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

const (
	exploreMaxSteps  = 6
	exploreMaxTokens = 4096
)

var exploreAllowedTools = map[string]bool{
	"code-search":       true,
	"code-read_file":    true,
	"code-symbols":      true,
	"code-definition":   true,
	"code-references":   true,
	"code-diagnostics":  true,
	"repo-search":       true,
	"repo-read_file":    true,
	"git-search_ref":    true,
	"git-read_file_ref": true,
	"rag-search":        true,
}

type ExploreTool struct {
	Manager *Manager
}

func (ExploreTool) Repeatable() bool { return true }

func (ExploreTool) Parallel() bool { return false }

func (t ExploreTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"explore-code",
		"",
		registry.ObjectSchema([]string{"task"}, map[string]any{
			"task":       map[string]any{"type": "string", "description": ""},
			"boundaries": map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (t ExploreTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	if t.Manager == nil || t.Manager.tools == nil {
		return registry.Result{}, fmt.Errorf("explore manager is not configured")
	}
	var args struct {
		Task       string `json:"task"`
		Boundaries string `json:"boundaries"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	task := strings.TrimSpace(args.Task)
	if task == "" {
		return registry.Result{}, fmt.Errorf("task is required")
	}
	out, err := t.Manager.Explore(ctx, task, strings.TrimSpace(args.Boundaries), rt)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: out}, nil
}

func (m *Manager) Explore(ctx context.Context, task, boundaries string, rt registry.Runtime) (string, error) {
	toolSpecs := m.exploreToolSpecs()
	if len(toolSpecs) == 0 {
		return "", fmt.Errorf("no read-only exploration tools are available")
	}
	user := "Investigation task:\n" + task
	if boundaries != "" {
		user += "\n\nBoundaries:\n" + boundaries
	}
	messages := []llm.Message{
		{Role: "system", Content: exploreSystemPrompt() + m.RulesAndSkillsPrompt()},
		{Role: "user", Content: user},
	}
	retriedSynthesis := false
	for step := 0; step < exploreMaxSteps; step++ {
		specs := toolSpecs
		if step == exploreMaxSteps-1 {
			specs = nil
		}
		resp, err := m.client.Chat(ctx, llm.Request{
			Model:       m.model,
			Thinking:    m.thinking,
			Messages:    messages,
			Tools:       specs,
			MaxTokens:   exploreMaxTokens,
			Temperature: 0.1,
		})
		if err != nil {
			return "", err
		}
		msg := resp.Message
		if len(msg.ToolCalls) == 0 {
			return strings.TrimSpace(llm.StripTextualToolCallMarkup(msg.Content)), nil
		}
		if len(specs) == 0 {
			if retriedSynthesis {
				return "", fmt.Errorf("explore did not produce a text report after synthesis retry")
			}
			messages = append(messages, llm.Message{Role: "system", Content: prompts.RunnerPrompt("synthesis_retry", "")})
			retriedSynthesis = true
			step--
			continue
		}
		msg.Content = ""
		messages = append(messages, msg)
		messages = append(messages, m.executeExploreToolCalls(ctx, msg.ToolCalls, rt)...)
	}
	return "", fmt.Errorf("explore did not converge within %d steps", exploreMaxSteps)
}

func (m *Manager) exploreToolSpecs() []llm.ToolSpec {
	specs := m.tools.Specs()
	out := make([]llm.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		if exploreAllowedTools[spec.Function.Name] {
			out = append(out, spec)
		}
	}
	return out
}

func exploreSystemPrompt() string {
	return prompts.Delegate("explore", `You are a read-only code exploration sub-agent.

Your job is to search and read code in an isolated context, then return a concise report to the main agent.

Rules:
- READ-ONLY MODE: never modify files and never request write/action tools.
- Use only the supplied read-only search/read/code-intelligence tools.
- For broad investigations, run independent searches or reads in parallel when possible.
- Prefer locating entry points, route mounts, state wiring, and call sites over repeatedly searching the same terms.
- If several searches miss, diagnose the failed assumption before widening.
- Stop once you have enough evidence for the main agent to synthesize.

Output format:
- Finding: one concise answer or orientation.
- Evidence: bullet list with file paths, line numbers, and exact facts from tool evidence.
- Excluded: what you checked and ruled out.
- Next decisive checks: at most 3 targeted reads/searches if uncertainty remains.`)
}

func (m *Manager) executeExploreToolCalls(ctx context.Context, calls []llm.ToolCall, rt registry.Runtime) []llm.Message {
	out := make([]llm.Message, len(calls))
	allParallel := len(calls) > 1
	for _, call := range calls {
		name := call.Function.Name
		if !exploreAllowedTools[name] || !m.tools.CanRunInParallel(name) {
			allParallel = false
			break
		}
	}
	if !allParallel {
		for i, call := range calls {
			out[i] = m.executeExploreToolCall(ctx, call, rt)
		}
		return out
	}
	var wg sync.WaitGroup
	wg.Add(len(calls))
	for i, call := range calls {
		go func(i int, call llm.ToolCall) {
			defer wg.Done()
			out[i] = m.executeExploreToolCall(ctx, call, rt)
		}(i, call)
	}
	wg.Wait()
	return out
}

func (m *Manager) executeExploreToolCall(ctx context.Context, call llm.ToolCall, rt registry.Runtime) llm.Message {
	name := call.Function.Name
	if !exploreAllowedTools[name] {
		return llm.Message{Role: "tool", ToolCallID: call.ID, Name: name, Content: "[tool error] tool is not available in read-only exploration"}
	}
	result, err := m.tools.Execute(ctx, name, json.RawMessage(call.Function.Arguments), rt)
	content := ""
	if err != nil {
		content = "[tool error] " + err.Error()
	} else {
		content = result.Content
	}
	if len(content) > 20000 {
		content = content[:10000] + "\n...[explore tool output truncated]...\n" + content[len(content)-9000:]
	}
	return llm.Message{Role: "tool", ToolCallID: call.ID, Name: name, Content: content}
}

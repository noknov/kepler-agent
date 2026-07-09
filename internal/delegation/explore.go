package delegation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

const (
	exploreMaxSteps  = 12
	exploreMaxTokens = 32768

	exploreMicroCompactThreshold = 6
	exploreKeepRecentToolResults = 4
	exploreToolResultClearedMsg  = "[Previous tool result cleared for context management]"
)

type ExploreTool struct {
	Manager *Manager
}

func (ExploreTool) Parallel() bool { return true }

func (t ExploreTool) CloneForRegistry(reg *registry.Registry) registry.Tool {
	t.Manager = t.Manager.WithTools(reg)
	return t
}

func (t ExploreTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"explore-code",
		"Run one or more read-only exploration sub-agents. Use tasks for independent investigation directions that can run in parallel.",
		registry.ObjectSchema(nil, map[string]any{
			"task":       map[string]any{"type": "string", "description": ""},
			"boundaries": map[string]any{"type": "string", "description": ""},
			"tasks": map[string]any{
				"type":        "array",
				"description": "Independent exploration jobs to run concurrently. Prefer 2-4 focused tasks over one broad task when latency matters.",
				"items": registry.ObjectSchema([]string{"task"}, map[string]any{
					"task":       map[string]any{"type": "string", "description": ""},
					"boundaries": map[string]any{"type": "string", "description": ""},
				}),
			},
		}),
	)
}

type ExploreJob struct {
	Task       string `json:"task"`
	Boundaries string `json:"boundaries"`
}

type exploreReport struct {
	index int
	job   ExploreJob
	out   string
	err   error
}

func (t ExploreTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	if t.Manager == nil || t.Manager.tools == nil {
		return registry.Result{}, fmt.Errorf("explore manager is not configured")
	}
	var args struct {
		Task       string       `json:"task"`
		Boundaries string       `json:"boundaries"`
		Tasks      []ExploreJob `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	jobs := normalizeExploreJobs(args.Task, args.Boundaries, args.Tasks)
	if len(jobs) == 0 {
		return registry.Result{}, fmt.Errorf("task or tasks is required")
	}
	if len(jobs) == 1 {
		out, err := t.Manager.Explore(ctx, jobs[0].Task, jobs[0].Boundaries, rt)
		if err != nil {
			return registry.Result{}, err
		}
		return registry.Result{Content: out}, nil
	}
	out, err := t.Manager.ExploreMany(ctx, jobs, rt)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: out}, nil
}

func normalizeExploreJobs(task, boundaries string, tasks []ExploreJob) []ExploreJob {
	var jobs []ExploreJob
	if task = strings.TrimSpace(task); task != "" {
		jobs = append(jobs, ExploreJob{Task: task, Boundaries: strings.TrimSpace(boundaries)})
	}
	for _, job := range tasks {
		job.Task = strings.TrimSpace(job.Task)
		job.Boundaries = strings.TrimSpace(job.Boundaries)
		if job.Task == "" {
			continue
		}
		jobs = append(jobs, job)
	}
	return jobs
}

func (m *Manager) Explore(ctx context.Context, task, boundaries string, rt registry.Runtime) (string, error) {
	toolSpecs := m.exploreToolSpecs()
	if len(toolSpecs) == 0 {
		return "", fmt.Errorf("no read-only exploration tools are available")
	}
	profile := m.exploreProfile()
	user := "Investigation task:\n" + task
	if boundaries != "" {
		user += "\n\nBoundaries:\n" + boundaries
	}
	messages := []llm.Message{
		{Role: "system", Content: profile.SystemPrompt + m.RulesAndSkillsPrompt()},
		{Role: "user", Content: user},
	}
	retriedSynthesis := false
	maxSteps := profile.MaxSteps
	for step := 0; step < maxSteps; step++ {
		specs := toolSpecs
		if step >= maxSteps-2 {
			specs = nil
		}
		resp, err := m.exploreChat(ctx, llm.Request{
			Model:       m.resolveSecondaryModel(),
			Thinking:    "", // disable thinking in explore for speed
			Messages:    messages,
			Tools:       specs,
			MaxTokens:   profile.MaxTokens,
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
				return partialExploreReport(messages), nil
			}
			messages = append(messages, llm.Message{Role: "system", Content: profile.FinalPrompt})
			retriedSynthesis = true
			continue
		}
		msg.Content = ""
		messages = append(messages, msg)
		messages = append(messages, m.executeExploreToolCalls(ctx, msg.ToolCalls, rt)...)
		messages = applyExploreMicroCompact(messages)
	}
	return "", fmt.Errorf("explore did not converge within %d steps", maxSteps)
}

func (m *Manager) ExploreMany(ctx context.Context, jobs []ExploreJob, rt registry.Runtime) (string, error) {
	jobs = normalizeExploreJobs("", "", jobs)
	if len(jobs) == 0 {
		return "", fmt.Errorf("at least one exploration task is required")
	}
	if len(jobs) == 1 {
		return m.Explore(ctx, jobs[0].Task, jobs[0].Boundaries, rt)
	}
	profile := m.exploreProfile()
	workers := profile.MaxWorkers
	if workers > len(jobs) {
		workers = len(jobs)
	}
	reports := make([]exploreReport, len(jobs))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	wg.Add(len(jobs))
	for i, job := range jobs {
		go func(i int, job ExploreJob) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				reports[i] = exploreReport{index: i, job: job, err: ctx.Err()}
				return
			}
			out, err := m.Explore(ctx, job.Task, job.Boundaries, rt)
			reports[i] = exploreReport{index: i, job: job, out: out, err: err}
		}(i, job)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return formatExploreReports(reports), nil
}

func formatExploreReports(reports []exploreReport) string {
	var b strings.Builder
	b.WriteString("Parallel exploration reports:")
	for _, report := range reports {
		b.WriteString("\n\n")
		b.WriteString(fmt.Sprintf("## Explorer %d: %s\n", report.index+1, report.job.Task))
		if report.job.Boundaries != "" {
			b.WriteString("Boundaries: ")
			b.WriteString(report.job.Boundaries)
			b.WriteString("\n")
		}
		if report.err != nil {
			b.WriteString("Finding: exploration failed.\nEvidence:\n- [tool error] ")
			b.WriteString(report.err.Error())
			b.WriteString("\nExcluded: not determined.\nNext decisive checks: retry this direction with narrower boundaries.")
			continue
		}
		out := strings.TrimSpace(report.out)
		if out == "" {
			out = "Finding: exploration produced no report.\nEvidence:\n- No evidence returned.\nExcluded: not determined.\nNext decisive checks: retry this direction with a narrower task."
		}
		b.WriteString(out)
	}
	return strings.TrimSpace(b.String())
}

func (m *Manager) exploreChat(ctx context.Context, req llm.Request) (llm.Response, error) {
	client := m.client
	sc := m.streamClient

	if m.secondaryClient != nil {
		client = m.secondaryClient
		sc = m.secondaryStreamClient
	}

	if sc == nil {
		if c, ok := client.(llm.StreamClient); ok {
			sc = c
		}
	}
	if sc != nil {
		return sc.ChatStream(ctx, req, llm.StreamHandler{})
	}
	return client.Chat(ctx, req)
}

func applyExploreMicroCompact(messages []llm.Message) []llm.Message {
	toolCount := 0
	for _, msg := range messages {
		if msg.Role == "tool" {
			toolCount++
		}
	}
	if toolCount <= exploreMicroCompactThreshold {
		return messages
	}

	keep := exploreKeepRecentToolResults
	seen := 0
	boundary := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "tool" {
			continue
		}
		seen++
		if seen >= keep {
			boundary = i
			break
		}
	}
	if boundary < 0 {
		return messages
	}

	out := make([]llm.Message, len(messages))
	copy(out, messages)
	cleared := false
	for i := 0; i < boundary; i++ {
		if out[i].Role != "tool" {
			continue
		}
		if out[i].Content == exploreToolResultClearedMsg {
			continue
		}
		out[i].Content = exploreToolResultClearedMsg
		cleared = true
	}
	if !cleared {
		return messages
	}
	return out
}

func partialExploreReport(messages []llm.Message) string {
	var evidence []string
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		text := strings.TrimSpace(msg.Content)
		if text == "" || text == exploreToolResultClearedMsg {
			continue
		}
		text = strings.Join(strings.Fields(text), " ")
		if len([]rune(text)) > 240 {
			runes := []rune(text)
			text = string(runes[:240]) + "..."
		}
		evidence = append(evidence, "- "+msg.Name+": "+text)
		if len(evidence) >= 6 {
			break
		}
	}
	if len(evidence) == 0 {
		return "Finding: exploration could not produce a final report.\nEvidence:\n- No tool evidence was gathered before synthesis failed.\nExcluded: none.\nNext decisive checks: run a narrower direct search/read in the main agent."
	}
	return "Finding: exploration stopped before producing a polished report; use this as partial evidence only.\nEvidence:\n" +
		strings.Join(evidence, "\n") +
		"\nExcluded: not fully determined.\nNext decisive checks: synthesize from these excerpts or run one narrower direct search/read."
}

func (m *Manager) exploreToolSpecs() []llm.ToolSpec {
	allowed := m.exploreProfile().AllowedTools
	specs := m.tools.Specs()
	out := make([]llm.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		if allowed[spec.Function.Name] {
			out = append(out, spec)
		}
	}
	return out
}

func (m *Manager) exploreProfile() ExploreProfile {
	if m == nil {
		return DefaultExploreProfile()
	}
	profile := m.explore
	if profile.MaxSteps <= 0 || profile.MaxTokens <= 0 || profile.Parallelism <= 0 || len(profile.AllowedTools) == 0 || profile.SystemPrompt == "" || profile.FinalPrompt == "" {
		m.SetExploreProfile(profile)
		profile = m.explore
	}
	return profile
}

func defaultExploreSystemPrompt() string {
	return `You are a read-only code exploration sub-agent.

Your job is to search and read code in an isolated context, then return a concise report to the main agent.

Rules:
- READ-ONLY MODE: never modify files and never request write/action tools.
- Use only the supplied read-only search/read/code-intelligence tools.
- For broad investigations, run independent searches or reads in parallel when possible.
- Prefer locating entry points, route mounts, state wiring, and call sites over repeatedly searching the same terms.
- If several searches miss, diagnose the failed assumption before widening.
- Treat semantic/RAG results as hints. Verify important claims with source reads before presenting them as evidence.
- Stop once you have enough evidence for the main agent to synthesize; do not keep searching for completeness after the controlling path is clear.

Output format:
- Finding: one concise answer or orientation.
- Confidence: high, medium, or low, with a short reason.
- Evidence: bullet list with file paths, line numbers, and exact facts from tool evidence. Include only facts you actually read.
- Excluded: what you checked and ruled out.
- Verification needed by main agent: critical claims that still need direct confirmation, or "none".
- Next decisive checks: at most 3 targeted reads/searches if uncertainty remains.`
}

func defaultExploreFinalReportPrompt() string {
	return `Tool calls are no longer available in this exploration sub-agent. Return a concise text report now using only evidence already gathered.

Output format:
- Finding: one concise answer or orientation.
- Confidence: high, medium, or low, with a short reason.
- Evidence: bullet list with file paths, line numbers, and exact facts from tool evidence. Include only facts you actually read.
- Excluded: what you checked and ruled out.
- Verification needed by main agent: critical claims that still need direct confirmation, or "none".
- Next decisive checks: at most 3 targeted reads/searches if uncertainty remains.`
}

func (m *Manager) executeExploreToolCalls(ctx context.Context, calls []llm.ToolCall, rt registry.Runtime) []llm.Message {
	out := make([]llm.Message, len(calls))
	if len(calls) == 1 {
		out[0] = m.executeExploreToolCall(ctx, calls[0], rt)
		return out
	}
	sem := make(chan struct{}, m.exploreProfile().Parallelism)
	var wg sync.WaitGroup
	wg.Add(len(calls))
	for i, call := range calls {
		go func(i int, call llm.ToolCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = m.executeExploreToolCall(ctx, call, rt)
		}(i, call)
	}
	wg.Wait()
	return out
}

func (m *Manager) executeExploreToolCall(ctx context.Context, call llm.ToolCall, rt registry.Runtime) llm.Message {
	name := call.Function.Name
	if !m.exploreProfile().AllowedTools[name] {
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

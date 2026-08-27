package delegation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/slack-copilot-agent/packages/agent/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
)

const (
	defaultExploreMaxSteps   = 12
	defaultExploreMaxWorkers = 3
)

const exploreSystemPrompt = `You are a read-only exploration sub-agent. Investigate the assigned task using only the provided tools. Do not mutate state, send messages, or request user input. Return a concise factual report with file paths, symbols, and evidence. Stop when you have enough to answer the task.`

// Runner executes isolated sub-turns against a filtered tool catalog.
type Runner struct {
	Config        agentruntime.Config
	Deps          agentruntime.Dependencies
	ParentCatalog *tool.Catalog
	AllowedTools  map[string]bool
	MaxSteps      int
	MaxWorkers    int
	Timeout       time.Duration
	SystemPrompt  string
}

type exploreJob struct {
	Task       string `json:"task"`
	Boundaries string `json:"boundaries"`
}

// ChildRun is durable audit metadata for one isolated exploration turn. Its
// transcript contains the full model and tool lifecycle; this record lets the
// parent tool result point to that evidence without putting it in model text.
type ChildRun struct {
	SessionID   string                         `json:"session_id"`
	TurnID      string                         `json:"turn_id"`
	Task        string                         `json:"task"`
	Termination agentruntime.TerminationReason `json:"termination,omitempty"`
	Usage       model.Usage                    `json:"usage"`
	Error       string                         `json:"error,omitempty"`
}

type childReport struct {
	Text  string
	Audit ChildRun
}

// ExploreTool runs one or more read-only sub-agents in parallel.
type ExploreTool struct {
	Runner Runner
}

func (t ExploreTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"agent-explore",
		"Run one or more read-only exploration sub-agents. Use tasks for independent investigation directions that can run in parallel.",
		tool.ObjectSchema(nil, map[string]any{
			"task": map[string]any{"type": "string", "description": "Single exploration task."},
			"boundaries": map[string]any{
				"type":        "string",
				"description": "Optional scope or constraints for a single task.",
			},
			"tasks": map[string]any{
				"type": "array",
				"items": tool.ObjectSchema([]string{"task"}, map[string]any{
					"task":       map[string]any{"type": "string"},
					"boundaries": map[string]any{"type": "string"},
				}),
				"description": "Independent exploration jobs to run concurrently.",
			},
		}),
		tool.WithEffects(tool.EffectRead),
		tool.WithParallel(true),
		tool.WithTimeout(t.Runner.timeout()),
	)
}

func (r Runner) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 2 * time.Minute
}

func (t ExploreTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Task       string       `json:"task"`
		Boundaries string       `json:"boundaries"`
		Tasks      []exploreJob `json:"tasks"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	jobs := normalizeJobs(args.Task, args.Boundaries, args.Tasks)
	if len(jobs) == 0 {
		return tool.Result{}, fmt.Errorf("task or tasks is required")
	}
	if len(jobs) == 1 {
		out, err := t.Runner.runJob(ctx, call, jobs[0])
		result := tool.TextResult(out.Text)
		result.Metadata = map[string]any{"child_runs": []ChildRun{out.Audit}}
		if err != nil {
			// Preserve the child-session link even when the exploration failed so
			// operators can inspect the durable transcript that caused the error.
			result.IsError = true
			result.ErrorCode = "explore_failed"
			result.Content = []model.Content{{Type: model.ContentText, Text: "Exploration failed: " + err.Error()}}
			return result, nil
		}
		return result, nil
	}
	reports, audits, err := t.Runner.runMany(ctx, call, jobs)
	if err != nil {
		return tool.Result{}, err
	}
	result := tool.TextResult(reports)
	result.Metadata = map[string]any{"child_runs": audits}
	return result, nil
}

func normalizeJobs(task, boundaries string, tasks []exploreJob) []exploreJob {
	var jobs []exploreJob
	if task = strings.TrimSpace(task); task != "" {
		jobs = append(jobs, exploreJob{Task: task, Boundaries: strings.TrimSpace(boundaries)})
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

func (r Runner) runMany(ctx context.Context, parentCall tool.Call, jobs []exploreJob) (string, []ChildRun, error) {
	workers := r.maxWorkers()
	sem := make(chan struct{}, workers)
	reports := make([]childReport, len(jobs))
	errs := make([]error, len(jobs))
	var wg sync.WaitGroup
	for index, job := range jobs {
		wg.Add(1)
		go func(i int, job exploreJob) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out, err := r.runJob(ctx, parentCall, job)
			reports[i] = out
			errs[i] = err
		}(index, job)
	}
	wg.Wait()
	var parts []string
	audits := make([]ChildRun, 0, len(jobs))
	for index, job := range jobs {
		if errs[index] != nil {
			parts = append(parts, fmt.Sprintf("## Task %d\n%s\n\nError: %v", index+1, job.Task, errs[index]))
			audits = append(audits, reports[index].Audit)
			continue
		}
		parts = append(parts, fmt.Sprintf("## Task %d\n%s\n\n%s", index+1, job.Task, reports[index].Text))
		audits = append(audits, reports[index].Audit)
	}
	return strings.Join(parts, "\n\n"), audits, nil
}

func (r Runner) runJob(ctx context.Context, parentCall tool.Call, job exploreJob) (childReport, error) {
	catalog, err := r.subsetCatalog()
	if err != nil {
		return childReport{}, err
	}
	if catalog == nil {
		return childReport{}, fmt.Errorf("no read-only exploration tools are available")
	}
	deps := r.Deps
	if deps.IDs == nil {
		deps.IDs = agentruntime.RandomIDs{}
	}
	deps.Tools = catalog
	if deps.Transcript == nil {
		deps.Transcript = transcript.NewMemoryStore()
	}
	// Child events are durable in their own transcript. Do not publish them to
	// a parent presentation sink, which could leak sub-agent stream deltas into
	// the user's turn or incorrectly charge them to the parent run projection.
	deps.Events = nil
	subRuntime, err := agentruntime.New(r.exploreConfig(), deps)
	if err != nil {
		return childReport{}, err
	}
	sessionID := deps.IDs.New("explore")
	turnID := deps.IDs.New("turn")
	input := "Investigation task:\n" + job.Task
	if job.Boundaries != "" {
		input += "\n\nBoundaries:\n" + job.Boundaries
	}
	scope := tool.Scope{
		SessionID: sessionID,
		TurnID:    turnID,
		UserID:    parentCall.Scope.UserID,
		Workspace: parentCall.Scope.Workspace,
		Values:    parentCall.Scope.Values,
	}
	result, err := subRuntime.RunTurn(ctx, agentruntime.TurnRequest{
		SessionID: sessionID,
		TurnID:    turnID,
		Input:     model.TextMessage(model.RoleUser, input),
		Prompt:    []prompt.Fragment{{ID: "explore-subagent", Layer: prompt.LayerCore, Content: r.systemPrompt()}},
		Scope:     scope,
		Model:     r.Config.Model,
		Parent:    &agentruntime.ParentLink{SessionID: parentCall.Scope.SessionID, TurnID: parentCall.Scope.TurnID, ToolCallID: parentCall.ID, Kind: "agent_explore"},
	})
	audit := ChildRun{SessionID: sessionID, TurnID: turnID, Task: job.Task, Termination: result.Termination, Usage: result.Usage}
	if err != nil {
		audit.Error = err.Error()
		return childReport{Audit: audit}, err
	}
	text := strings.TrimSpace(result.Message.Text())
	if text == "" {
		err := fmt.Errorf("exploration sub-agent returned an empty report")
		audit.Error = err.Error()
		return childReport{Audit: audit}, err
	}
	return childReport{Text: text, Audit: audit}, nil
}

func (r Runner) subsetCatalog() (*tool.Catalog, error) {
	if r.ParentCatalog == nil {
		return nil, fmt.Errorf("parent catalog is not configured")
	}
	allowed := r.AllowedTools
	if len(allowed) == 0 {
		allowed = DefaultHostedAllowedTools()
	}
	catalog, err := tool.NewCatalog()
	if err != nil {
		return nil, err
	}
	for name, enabled := range allowed {
		if !enabled || name == "agent-explore" {
			continue
		}
		item, ok := r.ParentCatalog.Get(name)
		if !ok {
			continue
		}
		if !isReadOnly(item.Descriptor()) {
			continue
		}
		if err := catalog.Register(item); err != nil {
			return nil, err
		}
	}
	if len(catalog.Descriptors()) == 0 {
		return nil, nil
	}
	return catalog, nil
}

func isReadOnly(descriptor tool.Descriptor) bool {
	for _, effect := range descriptor.Effects {
		if effect != tool.EffectRead && effect != tool.EffectNetwork {
			return false
		}
	}
	return true
}

func (r Runner) exploreConfig() agentruntime.Config {
	config := r.Config
	if r.MaxSteps > 0 {
		config.MaxSteps = r.MaxSteps
	} else {
		config.MaxSteps = defaultExploreMaxSteps
	}
	config.CircuitBreaker.Enabled = false
	return config
}

func (r Runner) maxWorkers() int {
	if r.MaxWorkers > 0 {
		return r.MaxWorkers
	}
	return defaultExploreMaxWorkers
}

func (r Runner) systemPrompt() string {
	if strings.TrimSpace(r.SystemPrompt) != "" {
		return r.SystemPrompt
	}
	return exploreSystemPrompt
}

// DefaultHostedAllowedTools lists read-only tools available to hosted explore jobs.
func DefaultHostedAllowedTools() map[string]bool {
	return map[string]bool{
		"code-search": true, "code-read_file": true, "code-symbols": true,
		"code-definition": true, "code-references": true, "code-diagnostics": true,
		"repo-search": true, "repo-read_file": true,
		"git-search_ref": true, "git-read_file_ref": true,
		"codegraph-overview": true, "codegraph-dependencies": true, "codegraph-symbols": true,
		"codegraph-definition": true, "codegraph-references": true, "codegraph-implementations": true,
		"codegraph-callers": true, "codegraph-callees": true, "codegraph-callgraph": true,
		"codegraph-impact": true, "web-search": true, "web-read_page": true,
	}
}

// DefaultLocalAllowedTools lists read-only tools for local explore jobs.
func DefaultLocalAllowedTools() map[string]bool {
	return map[string]bool{
		"read_file": true, "list_files": true, "search": true,
	}
}

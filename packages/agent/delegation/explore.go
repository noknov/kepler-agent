package delegation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

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
	SystemPrompt  string
}

type exploreJob struct {
	Task       string `json:"task"`
	Boundaries string `json:"boundaries"`
}

// ExploreTool runs one or more read-only sub-agents in parallel.
type ExploreTool struct {
	Runner Runner
}

func (ExploreTool) Descriptor() tool.Descriptor {
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
		tool.WithTimeout(0),
	)
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
		out, err := t.Runner.runJob(ctx, call.Scope, jobs[0])
		if err != nil {
			return tool.Result{}, err
		}
		return tool.TextResult(out), nil
	}
	reports, err := t.Runner.runMany(ctx, call.Scope, jobs)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(reports), nil
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

func (r Runner) runMany(ctx context.Context, scope tool.Scope, jobs []exploreJob) (string, error) {
	workers := r.maxWorkers()
	sem := make(chan struct{}, workers)
	reports := make([]string, len(jobs))
	errs := make([]error, len(jobs))
	var wg sync.WaitGroup
	for index, job := range jobs {
		wg.Add(1)
		go func(i int, job exploreJob) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out, err := r.runJob(ctx, scope, job)
			reports[i] = out
			errs[i] = err
		}(index, job)
	}
	wg.Wait()
	var parts []string
	for index, job := range jobs {
		if errs[index] != nil {
			parts = append(parts, fmt.Sprintf("## Task %d\n%s\n\nError: %v", index+1, job.Task, errs[index]))
			continue
		}
		parts = append(parts, fmt.Sprintf("## Task %d\n%s\n\n%s", index+1, job.Task, reports[index]))
	}
	return strings.Join(parts, "\n\n"), nil
}

func (r Runner) runJob(ctx context.Context, parentScope tool.Scope, job exploreJob) (string, error) {
	catalog, err := r.subsetCatalog()
	if err != nil {
		return "", err
	}
	if catalog == nil {
		return "", fmt.Errorf("no read-only exploration tools are available")
	}
	store := transcript.NewMemoryStore()
	deps := r.Deps
	if deps.IDs == nil {
		deps.IDs = agentruntime.RandomIDs{}
	}
	deps.Tools = catalog
	deps.Transcript = store
	deps.Events = nil
	subRuntime, err := agentruntime.New(r.exploreConfig(), deps)
	if err != nil {
		return "", err
	}
	sessionID := parentScope.SessionID + ":explore:" + deps.IDs.New("sub")
	turnID := deps.IDs.New("turn")
	input := "Investigation task:\n" + job.Task
	if job.Boundaries != "" {
		input += "\n\nBoundaries:\n" + job.Boundaries
	}
	scope := tool.Scope{
		SessionID: sessionID,
		TurnID:    turnID,
		UserID:    parentScope.UserID,
		Workspace: parentScope.Workspace,
		Values:    parentScope.Values,
	}
	result, err := subRuntime.RunTurn(ctx, agentruntime.TurnRequest{
		SessionID: sessionID,
		TurnID:    turnID,
		Input:     model.TextMessage(model.RoleUser, input),
		Prompt:    []prompt.Fragment{{ID: "explore-subagent", Layer: prompt.LayerCore, Content: r.systemPrompt()}},
		Scope:     scope,
		Model:     r.Config.Model,
	})
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(result.Message.Text())
	if text == "" {
		return "", fmt.Errorf("exploration sub-agent returned an empty report")
	}
	return text, nil
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

package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

const (
	defaultExploreMaxSteps   = 64
	defaultExploreMaxWorkers = 5
	defaultExploreMaxJobs    = 8
)

const exploreSystemPrompt = `You are a read-only exploration sub-agent. Investigate the assigned task using only the provided tools. Do not mutate state, send messages, or request user input. When independent reads or searches do not depend on each other, emit them in the same step (or pass multiple paths in one read) so they can run concurrently. Return a concise factual report with file paths, symbols, and evidence. Stop when you have enough to answer the task.`

// Runner executes isolated sub-turns against a filtered tool catalog.
type Runner struct {
	Config        agentruntime.Config
	Deps          agentruntime.Dependencies
	ParentCatalog *tool.Catalog
	AllowedTools  map[string]bool
	MaxSteps      int
	MaxWorkers    int
	MaxJobs       int
	Budget        ExecutionBudget
	SystemPrompt  string
}

// ExecutionBudget optionally places tighter bounds inside the parent turn.
// Zero values inherit the caller's deadline. When configured, BatchTimeout
// bounds the complete delegation call and WorkerTimeout starts only after a
// worker acquires a slot.
type ExecutionBudget struct {
	BatchTimeout  time.Duration
	WorkerTimeout time.Duration
}

// TaskSpec is the transport-neutral contract for one isolated agent worker.
// It deliberately describes the work instead of a named model or surface so
// workflows can reuse the same delegation infrastructure.
type TaskSpec struct {
	Name            string   `json:"name,omitempty"`
	Role            string   `json:"role,omitempty"`
	Task            string   `json:"task"`
	Boundaries      string   `json:"boundaries,omitempty"`
	Deliverable     string   `json:"deliverable,omitempty"`
	SuccessCriteria []string `json:"success_criteria,omitempty"`
}

// TaskRequest executes a worker outside the tool adapter. Product workflows
// can therefore compose the same child-agent primitive without depending on
// model-authored tool-call JSON.
type TaskRequest struct {
	Spec         TaskSpec
	Scope        tool.Scope
	Parent       *agentruntime.ParentLink
	SystemPrompt string
}

// TaskResult contains the compressed worker answer and its durable audit link.
type TaskResult struct {
	Message model.Message
	Audit   ChildRun
}

// ChildRun is durable audit metadata for one isolated exploration turn. Its
// transcript contains the full model and tool lifecycle; this record lets the
// parent tool result point to that evidence without putting it in model text.
type ChildRun struct {
	SessionID   string                         `json:"session_id"`
	TurnID      string                         `json:"turn_id"`
	Name        string                         `json:"name,omitempty"`
	Role        string                         `json:"role,omitempty"`
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
		"Run one or more isolated read-only worker agents. Use one tasks batch for independent directions that can run concurrently; give each worker a distinct role, scope, deliverable, and success criteria.",
		tool.ObjectSchema(nil, map[string]any{
			"name": map[string]any{"type": "string", "description": "Short stable worker name."},
			"role": map[string]any{"type": "string", "description": "Worker responsibility, distinct from its objective."},
			"task": map[string]any{"type": "string", "description": "Single exploration task."},
			"boundaries": map[string]any{
				"type":        "string",
				"description": "Optional scope or constraints for a single task.",
			},
			"deliverable": map[string]any{"type": "string", "description": "Required structured or textual output."},
			"success_criteria": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
				"description": "Observable conditions the worker must satisfy before returning.",
			},
			"tasks": map[string]any{
				"type":     "array",
				"maxItems": t.Runner.maxJobs(),
				"items": tool.ObjectSchema([]string{"task"}, map[string]any{
					"name":       map[string]any{"type": "string"},
					"role":       map[string]any{"type": "string"},
					"task":       map[string]any{"type": "string"},
					"boundaries": map[string]any{"type": "string"},
					"deliverable": map[string]any{
						"type": "string",
					},
					"success_criteria": map[string]any{
						"type": "array", "items": map[string]any{"type": "string"},
					},
				}),
				"description": "Independent worker assignments to run concurrently.",
			},
		}),
		tool.WithEffects(tool.EffectRead),
		tool.WithParallel(true),
		tool.WithTimeout(t.Runner.batchTimeout()),
	)
}

func (r Runner) batchTimeout() time.Duration {
	return r.Budget.BatchTimeout
}

func (r Runner) workerTimeout() time.Duration {
	return r.Budget.WorkerTimeout
}

func (t ExploreTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Name            string     `json:"name"`
		Role            string     `json:"role"`
		Task            string     `json:"task"`
		Boundaries      string     `json:"boundaries"`
		Deliverable     string     `json:"deliverable"`
		SuccessCriteria []string   `json:"success_criteria"`
		Tasks           []TaskSpec `json:"tasks"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	jobs := normalizeJobs(TaskSpec{Name: args.Name, Role: args.Role, Task: args.Task, Boundaries: args.Boundaries, Deliverable: args.Deliverable, SuccessCriteria: args.SuccessCriteria}, args.Tasks)
	if len(jobs) == 0 {
		return tool.Result{}, fmt.Errorf("task or tasks is required")
	}
	if len(jobs) > t.Runner.maxJobs() {
		return tool.Result{}, fmt.Errorf("too many exploration jobs: got %d, maximum is %d", len(jobs), t.Runner.maxJobs())
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

func normalizeJobs(single TaskSpec, tasks []TaskSpec) []TaskSpec {
	var jobs []TaskSpec
	if single.Task = strings.TrimSpace(single.Task); single.Task != "" {
		jobs = append(jobs, normalizeTask(single))
	}
	for _, job := range tasks {
		job = normalizeTask(job)
		if job.Task == "" {
			continue
		}
		jobs = append(jobs, job)
	}
	return jobs
}

func normalizeTask(job TaskSpec) TaskSpec {
	job.Name = strings.TrimSpace(job.Name)
	job.Role = strings.TrimSpace(job.Role)
	job.Task = strings.TrimSpace(job.Task)
	job.Boundaries = strings.TrimSpace(job.Boundaries)
	job.Deliverable = strings.TrimSpace(job.Deliverable)
	criteria := make([]string, 0, len(job.SuccessCriteria))
	for _, criterion := range job.SuccessCriteria {
		if criterion = strings.TrimSpace(criterion); criterion != "" {
			criteria = append(criteria, criterion)
		}
	}
	job.SuccessCriteria = criteria
	return job
}

func (r Runner) runMany(ctx context.Context, parentCall tool.Call, jobs []TaskSpec) (string, []ChildRun, error) {
	workers := r.maxWorkers()
	sem := make(chan struct{}, workers)
	reports := make([]childReport, len(jobs))
	errs := make([]error, len(jobs))
	var wg sync.WaitGroup
	for index, job := range jobs {
		wg.Add(1)
		go func(i int, job TaskSpec) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				err := ctx.Err()
				errs[i] = err
				reports[i].Audit = ChildRun{Task: job.Task, Error: err.Error()}
				return
			}
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
			parts = append(parts, fmt.Sprintf("## %s\n%s\n\nError: %v", taskLabel(index, job), job.Task, errs[index]))
			audits = append(audits, reports[index].Audit)
			continue
		}
		parts = append(parts, fmt.Sprintf("## %s\n%s\n\n%s", taskLabel(index, job), job.Task, reports[index].Text))
		audits = append(audits, reports[index].Audit)
	}
	return strings.Join(parts, "\n\n"), audits, nil
}

func taskLabel(index int, job TaskSpec) string {
	if job.Name != "" && job.Role != "" {
		return job.Name + " · " + job.Role
	}
	if job.Name != "" {
		return job.Name
	}
	if job.Role != "" {
		return job.Role
	}
	return fmt.Sprintf("Task %d", index+1)
}

func (r Runner) runJob(ctx context.Context, parentCall tool.Call, job TaskSpec) (childReport, error) {
	result, err := r.RunTask(ctx, TaskRequest{
		Spec:  job,
		Scope: parentCall.Scope,
		Parent: &agentruntime.ParentLink{
			SessionID:  parentCall.Scope.SessionID,
			TurnID:     parentCall.Scope.TurnID,
			ToolCallID: parentCall.ID,
			Kind:       "agent_explore",
		},
	})
	return childReport{Text: strings.TrimSpace(result.Message.Text()), Audit: result.Audit}, err
}

// RunTask runs one isolated child agent through the shared Kepler runtime.
func (r Runner) RunTask(ctx context.Context, request TaskRequest) (TaskResult, error) {
	if timeout := r.workerTimeout(); timeout > 0 {
		workerCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		ctx = workerCtx
	}
	job := normalizeTask(request.Spec)
	if job.Task == "" {
		return TaskResult{}, fmt.Errorf("task is required")
	}
	catalog, err := r.subsetCatalog()
	if err != nil {
		return TaskResult{}, err
	}
	if catalog == nil {
		return TaskResult{}, fmt.Errorf("no read-only exploration tools are available")
	}
	deps := r.Deps
	if deps.IDs == nil {
		deps.IDs = agentruntime.RandomIDs{}
	}
	deps.Tools = catalog
	if deps.Transcript == nil {
		deps.Transcript = transcript.NewMemoryStore()
	}
	parentEvents := deps.Events
	// Child events are durable in their own transcript. Do not publish them to
	// a parent presentation sink, which could leak sub-agent stream deltas into
	// the user's turn or incorrectly charge them to the parent run projection.
	deps.Events = nil
	subRuntime, err := agentruntime.New(r.exploreConfig(), deps)
	if err != nil {
		return TaskResult{}, err
	}
	sessionID := deps.IDs.New("explore")
	turnID := deps.IDs.New("turn")
	audit := ChildRun{SessionID: sessionID, TurnID: turnID, Name: job.Name, Role: job.Role, Task: job.Task}
	if err := r.publishTaskEvent(ctx, parentEvents, request, transcript.DelegatedTaskStarted, audit, nil); err != nil {
		audit.Error = err.Error()
		return TaskResult{Audit: audit}, fmt.Errorf("record delegated task start: %w", err)
	}
	input := taskInput(job)
	scope := tool.Scope{
		SessionID: sessionID,
		TurnID:    turnID,
		UserID:    request.Scope.UserID,
		Workspace: request.Scope.Workspace,
		Values:    request.Scope.Values,
	}
	systemPrompt := r.systemPrompt()
	if strings.TrimSpace(request.SystemPrompt) != "" {
		systemPrompt = strings.TrimSpace(request.SystemPrompt)
	}
	result, err := subRuntime.RunTurn(ctx, agentruntime.TurnRequest{
		SessionID: sessionID,
		TurnID:    turnID,
		Input:     model.TextMessage(model.RoleUser, input),
		Prompt:    []prompt.Fragment{{ID: "delegated-worker", Layer: prompt.LayerCore, Content: systemPrompt}},
		Scope:     scope,
		Model:     r.Config.Model,
		Parent:    request.Parent,
	})
	audit.Termination = result.Termination
	audit.Usage = result.Usage
	if err != nil {
		audit.Error = err.Error()
		publishErr := r.publishTaskEvent(ctx, parentEvents, request, transcript.DelegatedTaskFailed, audit, nil)
		return TaskResult{Audit: audit}, errors.Join(err, publishErr)
	}
	text := strings.TrimSpace(result.Message.Text())
	if text == "" {
		err := fmt.Errorf("exploration sub-agent returned an empty report")
		audit.Error = err.Error()
		publishErr := r.publishTaskEvent(ctx, parentEvents, request, transcript.DelegatedTaskFailed, audit, nil)
		return TaskResult{Audit: audit}, errors.Join(err, publishErr)
	}
	if err := r.publishTaskEvent(ctx, parentEvents, request, transcript.DelegatedTaskCompleted, audit, &result.Message); err != nil {
		audit.Error = err.Error()
		return TaskResult{Message: result.Message, Audit: audit}, fmt.Errorf("record delegated task completion: %w", err)
	}
	return TaskResult{Message: result.Message, Audit: audit}, nil
}

func (r Runner) publishTaskEvent(ctx context.Context, sink transcript.Sink, request TaskRequest, eventType transcript.EventType, audit ChildRun, message *model.Message) error {
	if request.Parent == nil || r.Deps.Transcript == nil {
		return nil
	}
	metadata, err := json.Marshal(map[string]any{"child_run": audit})
	if err != nil {
		return err
	}
	ids := r.Deps.IDs
	if ids == nil {
		ids = agentruntime.RandomIDs{}
	}
	event, err := r.Deps.Transcript.Append(ctx, transcript.Event{
		ID: ids.New("event"), SessionID: request.Parent.SessionID, TurnID: request.Parent.TurnID,
		Type: eventType, Timestamp: time.Now().UTC(), Message: message, Metadata: metadata,
	})
	if err != nil {
		return err
	}
	if sink != nil {
		sink.Publish(ctx, event)
	}
	return nil
}

func taskInput(job TaskSpec) string {
	var input strings.Builder
	input.WriteString("Investigation task:\n")
	input.WriteString(job.Task)
	if job.Role != "" {
		input.WriteString("\n\nAssigned role:\n")
		input.WriteString(job.Role)
	}
	if job.Boundaries != "" {
		input.WriteString("\n\nBoundaries:\n")
		input.WriteString(job.Boundaries)
	}
	if job.Deliverable != "" {
		input.WriteString("\n\nRequired deliverable:\n")
		input.WriteString(job.Deliverable)
	}
	if len(job.SuccessCriteria) > 0 {
		input.WriteString("\n\nSuccess criteria:")
		for _, criterion := range job.SuccessCriteria {
			input.WriteString("\n- ")
			input.WriteString(criterion)
		}
	}
	return input.String()
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
		// This catalog is already an explicit, read-only capability subset.
		// Promote selected deferred tools because the isolated child runtime does
		// not carry the parent's tool-search activation state.
		item = tool.Annotate(item, tool.Descriptor{Exposure: tool.ExposureEager})
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

func (r Runner) maxJobs() int {
	if r.MaxJobs > 0 {
		return r.MaxJobs
	}
	return defaultExploreMaxJobs
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
		"code-search": true, "code-read_file": true,
		"repo-search": true, "repo-read_file": true,
		"git-search_ref": true, "git-read_file_ref": true,
		"web-search": true, "web-read_page": true,
		"github-pr_diff": true, "github-pr_file_diff": true,
	}
}

// DefaultLocalAllowedTools lists read-only tools for local explore jobs.
func DefaultLocalAllowedTools() map[string]bool {
	return map[string]bool{
		"read_file": true, "list_files": true, "search": true,
	}
}

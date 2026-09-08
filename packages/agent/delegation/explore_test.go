package delegation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type echoReadTool struct{}

func (echoReadTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "search", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []tool.Effect{tool.EffectRead}, Parallel: true}
}
func (echoReadTool) Execute(_ context.Context, _ tool.Call) (tool.Result, error) {
	return tool.TextResult("found evidence"), nil
}

type deferredReadTool struct{}

func (deferredReadTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "deferred-read", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []tool.Effect{tool.EffectRead}, Exposure: tool.ExposureDeferred}
}
func (deferredReadTool) Execute(_ context.Context, _ tool.Call) (tool.Result, error) {
	return tool.TextResult("deferred evidence"), nil
}

type scriptedExploreModel struct{ text string }

func (m scriptedExploreModel) Generate(_ context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	return model.Response{Message: model.TextMessage(model.RoleAssistant, m.text), FinishReason: model.FinishStop}, nil
}

type capturingExploreModel struct{ request *model.Request }

func leafBatch(tasks ...string) json.RawMessage {
	jobs := make([]TaskSpec, 0, len(tasks))
	for index, task := range tasks {
		jobs = append(jobs, TaskSpec{
			Name: fmt.Sprintf("worker-%d", index+1), Role: "independent reviewer", Task: task,
			Boundaries: task, Deliverable: "factual evidence", SuccessCriteria: []string{"return evidence to the lead"},
		})
	}
	data, _ := json.Marshal(map[string]any{"tasks": jobs})
	return data
}

func (m *capturingExploreModel) Generate(_ context.Context, request model.Request, _ model.EventSink) (model.Response, error) {
	m.request = &request
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "structured report"), FinishReason: model.FinishStop}, nil
}

type failingExploreModel struct{}

func (failingExploreModel) Generate(_ context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	return model.Response{}, context.DeadlineExceeded
}

type deadlineRecordingModel struct {
	mu           sync.Mutex
	remaining    []time.Duration
	firstStarted chan struct{}
	releaseFirst chan struct{}
}

func (m *deadlineRecordingModel) Generate(ctx context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return model.Response{}, context.DeadlineExceeded
	}
	m.mu.Lock()
	index := len(m.remaining)
	m.remaining = append(m.remaining, time.Until(deadline))
	m.mu.Unlock()
	if index == 0 {
		close(m.firstStarted)
		select {
		case <-m.releaseFirst:
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "report"), FinishReason: model.FinishStop}, nil
}

func TestExploreToolRunsParallelJobs(t *testing.T) {
	parent, err := tool.NewCatalog(echoReadTool{})
	if err != nil {
		t.Fatal(err)
	}
	store := transcript.NewMemoryStore()
	runner := Runner{
		Config:        agentruntime.Config{Model: "test"},
		Deps:          agentruntime.Dependencies{Model: scriptedExploreModel{text: "report"}, IDs: agentruntime.RandomIDs{}, Transcript: store},
		ParentCatalog: parent,
		AllowedTools:  DefaultLocalAllowedTools(),
	}
	explore := ExploreTool{Runner: runner}
	result, err := explore.Execute(context.Background(), tool.Call{
		Arguments: leafBatch("find auth", "find billing"),
		Scope:     tool.Scope{SessionID: "ses_test", TurnID: "turn_parent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := result.Text()
	if text == "" || !strings.Contains(text, "worker-1") || !strings.Contains(text, "worker-2") || !strings.Contains(text, "report") {
		t.Fatalf("unexpected explore output: %q", text)
	}
	children, ok := result.Metadata["child_runs"].([]ChildRun)
	if !ok || len(children) != 2 {
		t.Fatalf("child audit metadata = %#v", result.Metadata)
	}
	for _, child := range children {
		events, loadErr := store.Load(context.Background(), child.SessionID, 0)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if len(events) == 0 {
			t.Fatalf("child %s has no durable transcript", child.SessionID)
		}
		var metadata struct {
			Parent agentruntime.ParentLink `json:"parent"`
		}
		if err := json.Unmarshal(events[1].Metadata, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.Parent.SessionID != "ses_test" || metadata.Parent.TurnID != "turn_parent" || metadata.Parent.Kind != "agent_explore" {
			t.Fatalf("child parent metadata = %#v", metadata.Parent)
		}
	}
}

func TestExploreToolInjectsWorkflowSharedContextIntoWorker(t *testing.T) {
	parent, err := tool.NewCatalog(echoReadTool{})
	if err != nil {
		t.Fatal(err)
	}
	client := &capturingExploreModel{}
	explore := ExploreTool{Runner: Runner{
		Config:        agentruntime.Config{Model: "test"},
		Deps:          agentruntime.Dependencies{Model: client, Transcript: transcript.NewMemoryStore()},
		ParentCatalog: parent,
		AllowedTools:  DefaultLocalAllowedTools(),
	}}
	_, err = explore.Execute(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"task":"inspect changed behavior"}`),
		Scope: tool.Scope{SessionID: "parent", TurnID: "turn", Values: map[string]string{
			ScopeSharedContext: "PR URLs:\n- https://github.com/acme/widgets/pull/42",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := client.request.Messages[len(client.request.Messages)-1].Text()
	for _, want := range []string{"Authoritative context supplied by the parent workflow", "https://github.com/acme/widgets/pull/42", "Investigation task:\ninspect changed behavior"} {
		if !strings.Contains(input, want) {
			t.Fatalf("worker input missing %q: %s", want, input)
		}
	}
}

func TestQueuedWorkerReceivesFreshExecutionBudgetAfterAcquiringSlot(t *testing.T) {
	parent, err := tool.NewCatalog(echoReadTool{})
	if err != nil {
		t.Fatal(err)
	}
	client := &deadlineRecordingModel{firstStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	runner := Runner{
		Config:        agentruntime.Config{Model: "test"},
		Deps:          agentruntime.Dependencies{Model: client, Transcript: transcript.NewMemoryStore()},
		ParentCatalog: parent,
		AllowedTools:  DefaultLocalAllowedTools(),
		MaxWorkers:    1,
		Budget:        ExecutionBudget{WorkerTimeout: 500 * time.Millisecond, BatchTimeout: time.Second},
	}
	done := make(chan error, 1)
	go func() {
		_, executeErr := (ExploreTool{Runner: runner}).Execute(context.Background(), tool.Call{
			Arguments: leafBatch("first", "second"),
			Scope:     tool.Scope{SessionID: "parent", TurnID: "turn"},
		})
		done <- executeErr
	}()
	select {
	case <-client.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first worker did not start")
	}
	time.Sleep(250 * time.Millisecond)
	close(client.releaseFirst)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	remaining := append([]time.Duration(nil), client.remaining...)
	client.mu.Unlock()
	if len(remaining) != 2 {
		t.Fatalf("worker model calls = %d, want 2", len(remaining))
	}
	if remaining[1] < 400*time.Millisecond {
		t.Fatalf("queued worker inherited queue delay: remaining budget = %s", remaining[1])
	}
}

func TestExploreToolDescriptorUsesBatchBudget(t *testing.T) {
	if timeout := (ExploreTool{Runner: Runner{}}).Descriptor().Timeout; timeout != 0 {
		t.Fatalf("default descriptor timeout = %s, want inherited parent deadline", timeout)
	}
	descriptor := (ExploreTool{Runner: Runner{Budget: ExecutionBudget{WorkerTimeout: time.Minute, BatchTimeout: 3 * time.Minute}}}).Descriptor()
	if descriptor.Timeout != 3*time.Minute {
		t.Fatalf("descriptor timeout = %s, want 3m", descriptor.Timeout)
	}
}

func TestExploreConfigUsesConfiguredStepGuard(t *testing.T) {
	defaults := (Runner{}).exploreConfig()
	if defaults.MaxSteps != 64 {
		t.Fatalf("default explore step limit = %d, want 64", defaults.MaxSteps)
	}
	explicit := (Runner{MaxSteps: 40}).exploreConfig()
	if explicit.MaxSteps != 40 {
		t.Fatalf("explicit explore step limit = %d, want 40", explicit.MaxSteps)
	}
}

func TestExploreToolPreservesChildAuditWhenJobFails(t *testing.T) {
	parent, err := tool.NewCatalog(echoReadTool{})
	if err != nil {
		t.Fatal(err)
	}
	store := transcript.NewMemoryStore()
	explore := ExploreTool{Runner: Runner{
		Config:        agentruntime.Config{Model: "test", MaxModelRetries: 0},
		Deps:          agentruntime.Dependencies{Model: failingExploreModel{}, IDs: agentruntime.RandomIDs{}, Transcript: store},
		ParentCatalog: parent,
		AllowedTools:  DefaultLocalAllowedTools(),
	}}
	result, err := explore.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{"task":"find auth"}`), Scope: tool.Scope{SessionID: "ses_parent", TurnID: "turn_parent"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.ErrorCode != "explore_failed" {
		t.Fatalf("result = %#v", result)
	}
	children, ok := result.Metadata["child_runs"].([]ChildRun)
	if !ok || len(children) != 1 || children[0].Error == "" {
		t.Fatalf("child audit metadata = %#v", result.Metadata)
	}
	events, err := store.Load(context.Background(), children[0].SessionID, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestSubsetCatalogPromotesAllowedDeferredTool(t *testing.T) {
	parent, err := tool.NewCatalog(deferredReadTool{})
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{ParentCatalog: parent, AllowedTools: map[string]bool{"deferred-read": true}}
	catalog, err := runner.subsetCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.GetActive("child-session", "deferred-read"); !ok {
		t.Fatal("allowed deferred tool is not active in child catalog")
	}
}

func TestExploreToolRejectsTooManyJobs(t *testing.T) {
	parent, err := tool.NewCatalog(echoReadTool{})
	if err != nil {
		t.Fatal(err)
	}
	explore := ExploreTool{Runner: Runner{
		Config:        agentruntime.Config{Model: "test"},
		Deps:          agentruntime.Dependencies{Model: scriptedExploreModel{text: "report"}},
		ParentCatalog: parent,
		AllowedTools:  DefaultLocalAllowedTools(),
		MaxJobs:       1,
	}}
	_, err = explore.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{"tasks":[{"task":"one"},{"task":"two"}]}`)})
	if err == nil || !strings.Contains(err.Error(), "too many exploration jobs") {
		t.Fatalf("expected job limit error, got %v", err)
	}
}

func TestExploreToolRunsCompleteLeafTasksAsOneConcurrentTeam(t *testing.T) {
	parent, err := tool.NewCatalog(echoReadTool{})
	if err != nil {
		t.Fatal(err)
	}
	explore := ExploreTool{Runner: Runner{
		Config:        agentruntime.Config{Model: "test"},
		Deps:          agentruntime.Dependencies{Model: scriptedExploreModel{text: "report"}, Transcript: transcript.NewMemoryStore()},
		ParentCatalog: parent,
		AllowedTools:  DefaultLocalAllowedTools(),
	}}
	complete := `{"tasks":[` +
		`{"name":"auth","role":"security reviewer","task":"inspect auth","boundaries":"auth changes","deliverable":"evidence","success_criteria":["cite changed lines"]},` +
		`{"name":"data","role":"data reviewer","task":"inspect writes","boundaries":"persistence changes","deliverable":"evidence","success_criteria":["cite changed lines"]}` +
		`]}`
	result, err := explore.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(complete)})
	if err != nil {
		t.Fatal(err)
	}
	children, ok := result.Metadata["child_runs"].([]ChildRun)
	if !ok || len(children) != 2 || children[0].Name == children[1].Name {
		t.Fatalf("children=%#v", result.Metadata["child_runs"])
	}
}

func TestValidateLeafTasksRequiresCompleteUniqueContracts(t *testing.T) {
	valid := TaskSpec{Name: "auth", Role: "reviewer", Task: "inspect auth", Boundaries: "auth", Deliverable: "evidence", SuccessCriteria: []string{"cite lines"}}
	if err := validateLeafTasks([]TaskSpec{valid, {Name: "data", Role: "reviewer", Task: "inspect data"}}); err == nil || !strings.Contains(err.Error(), "requires name") {
		t.Fatalf("incomplete task error = %v", err)
	}
	if err := validateLeafTasks([]TaskSpec{valid, valid}); err == nil || !strings.Contains(err.Error(), "must be unique") {
		t.Fatalf("duplicate task error = %v", err)
	}
}

func TestRunTaskCarriesWorkerContractAndAuditIdentity(t *testing.T) {
	parent, err := tool.NewCatalog(echoReadTool{})
	if err != nil {
		t.Fatal(err)
	}
	client := &capturingExploreModel{}
	runner := Runner{
		Config:        agentruntime.Config{Model: "test"},
		Deps:          agentruntime.Dependencies{Model: client, Transcript: transcript.NewMemoryStore()},
		ParentCatalog: parent,
		AllowedTools:  DefaultLocalAllowedTools(),
	}
	result, err := runner.RunTask(context.Background(), TaskRequest{
		Spec: TaskSpec{
			Name: "auth-boundary", Role: "Security reviewer", Task: "Review authentication changes",
			Boundaries: "Only changed request paths", Deliverable: "JSON findings",
			SuccessCriteria: []string{"Cite path and line", "Return no finding without evidence"},
		},
		Scope:         tool.Scope{UserID: "U1", Workspace: "/repo"},
		SharedContext: "Repository: acme/widgets",
		Parent:        &agentruntime.ParentLink{SessionID: "parent", TurnID: "turn", Kind: "workflow"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Audit.Name != "auth-boundary" || result.Audit.Role != "Security reviewer" || result.Message.Text() != "structured report" {
		t.Fatalf("result=%+v", result)
	}
	if client.request == nil || len(client.request.Messages) < 2 {
		t.Fatalf("model request=%+v", client.request)
	}
	input := client.request.Messages[len(client.request.Messages)-1].Text()
	for _, want := range []string{"Authoritative context supplied by the parent workflow:\nRepository: acme/widgets", "Assigned role:\nSecurity reviewer", "Required deliverable:\nJSON findings", "- Cite path and line"} {
		if !strings.Contains(input, want) {
			t.Fatalf("worker input missing %q: %s", want, input)
		}
	}
}

func TestRunTaskPublishesCanonicalDelegationLifecycle(t *testing.T) {
	parent, err := tool.NewCatalog(echoReadTool{})
	if err != nil {
		t.Fatal(err)
	}
	store := transcript.NewMemoryStore()
	var published []transcript.Event
	runner := Runner{
		Config: agentruntime.Config{Model: "test"},
		Deps: agentruntime.Dependencies{
			Model: scriptedExploreModel{text: "candidate"}, Transcript: store,
			Events: transcript.SinkFunc(func(_ context.Context, event transcript.Event) { published = append(published, event) }),
		},
		ParentCatalog: parent,
		AllowedTools:  DefaultLocalAllowedTools(),
	}
	_, err = runner.RunTask(context.Background(), TaskRequest{
		Spec:   TaskSpec{Name: "auth", Role: "Security reviewer", Task: "check auth"},
		Scope:  tool.Scope{},
		Parent: &agentruntime.ParentLink{SessionID: "parent", TurnID: "review", Kind: "agent_explore"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 2 || published[0].Type != transcript.DelegatedTaskStarted || published[1].Type != transcript.DelegatedTaskCompleted {
		t.Fatalf("published=%+v", published)
	}
	events, err := store.Load(context.Background(), "parent", 0)
	if err != nil || len(events) != 2 || events[1].Message == nil || events[1].Message.Text() != "candidate" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

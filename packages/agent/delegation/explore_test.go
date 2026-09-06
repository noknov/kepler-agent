package delegation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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

func (m *capturingExploreModel) Generate(_ context.Context, request model.Request, _ model.EventSink) (model.Response, error) {
	m.request = &request
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "structured report"), FinishReason: model.FinishStop}, nil
}

type failingExploreModel struct{}

func (failingExploreModel) Generate(_ context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	return model.Response{}, context.DeadlineExceeded
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
		Arguments: json.RawMessage(`{"tasks":[{"task":"find auth"},{"task":"find billing"}]}`),
		Scope:     tool.Scope{SessionID: "ses_test", TurnID: "turn_parent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := result.Text()
	if text == "" || !strings.Contains(text, "Task 1") || !strings.Contains(text, "Task 2") || !strings.Contains(text, "report") {
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
		Scope:  tool.Scope{UserID: "U1", Workspace: "/repo"},
		Parent: &agentruntime.ParentLink{SessionID: "parent", TurnID: "turn", Kind: "workflow"},
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
	for _, want := range []string{"Assigned role:\nSecurity reviewer", "Required deliverable:\nJSON findings", "- Cite path and line"} {
		if !strings.Contains(input, want) {
			t.Fatalf("worker input missing %q: %s", want, input)
		}
	}
}

func TestRunTaskPublishesCanonicalPersonaLifecycleForOptedInWorkflow(t *testing.T) {
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
		Scope:  tool.Scope{Values: map[string]string{"delegation_presentation": "persona"}},
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

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

type scriptedExploreModel struct{ text string }

func (m scriptedExploreModel) Generate(_ context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	return model.Response{Message: model.TextMessage(model.RoleAssistant, m.text), FinishReason: model.FinishStop}, nil
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

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

type investigatingModel struct{ calls int }

func (m *investigatingModel) Generate(context.Context, model.Request, model.EventSink) (model.Response, error) {
	m.calls++
	if m.calls == 1 {
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "search", Name: "search", Arguments: json.RawMessage(`{}`)}}}}, FinishReason: model.FinishToolCalls}, nil
	}
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "report"), FinishReason: model.FinishStop}, nil
}
func TestChildEvidenceUsesParentDelegationAuthorityAndCanonicalResults(t *testing.T) {
	ctx := context.Background()
	store := transcript.NewMemoryStore()
	catalog, _ := tool.NewCatalog(echoReadTool{})
	runner := Runner{Config: agentruntime.Config{Model: "test"}, Deps: agentruntime.Dependencies{Model: &investigatingModel{}, Transcript: store}, ParentCatalog: catalog, AllowedTools: map[string]bool{"search": true}}
	result, err := runner.RunTask(ctx, TaskRequest{Spec: TaskSpec{Task: "investigate"}, Scope: tool.Scope{SessionID: "parent"}, Parent: &agentruntime.ParentLink{SessionID: "parent", TurnID: "turn", ToolCallID: "explore"}})
	if err != nil {
		t.Fatal(err)
	}
	audit := result.Audit
	if audit.ParentSequence == 0 || audit.EvidenceCount != 1 || len(audit.Evidence) != 1 || audit.Evidence[0].SHA256 == "" {
		t.Fatalf("missing evidence manifest: %+v", audit)
	}
	reader := EvidenceTool{Store: store}
	args, _ := json.Marshal(map[string]any{"parent_sequence": audit.ParentSequence, "sequence": audit.Evidence[0].Sequence})
	call := tool.Call{Scope: tool.Scope{SessionID: "parent"}, Arguments: args}
	read, err := reader.Execute(ctx, call)
	if err != nil || !strings.Contains(read.Text(), "found evidence") {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	call.Scope.SessionID = "other"
	if _, err := reader.Execute(ctx, call); err == nil {
		t.Fatal("unrelated parent read child evidence")
	}
	// An arbitrary assistant/model item cannot mint a child capability.
	fake, _ := store.Append(ctx, transcript.Event{SessionID: "other", Type: transcript.AssistantMessage, Metadata: json.RawMessage(`{"child_run":{"session_id":"` + audit.SessionID + `","turn_id":"` + audit.TurnID + `"}}`)})
	call.Arguments, _ = json.Marshal(map[string]any{"parent_sequence": fake.Sequence, "sequence": audit.Evidence[0].Sequence})
	if _, err := reader.Execute(ctx, call); err == nil {
		t.Fatal("model-authored ancestry accepted")
	}
}
func TestStepLimitedChildIsNotPublishedAsCompleted(t *testing.T) {
	store := transcript.NewMemoryStore()
	catalog, _ := tool.NewCatalog(echoReadTool{})
	runner := Runner{MaxSteps: 1, Config: agentruntime.Config{Model: "test"}, Deps: agentruntime.Dependencies{Model: &investigatingModel{}, Transcript: store}, ParentCatalog: catalog, AllowedTools: map[string]bool{"search": true}}
	result, err := runner.RunTask(context.Background(), TaskRequest{Spec: TaskSpec{Task: "investigate"}, Parent: &agentruntime.ParentLink{SessionID: "parent", TurnID: "turn"}})
	if err == nil || result.Audit.Termination != agentruntime.TerminationMaxSteps {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	events, _ := store.Load(context.Background(), "parent", 0)
	for _, event := range events {
		if event.Type == transcript.DelegatedTaskCompleted {
			t.Fatal("partial child was marked completed")
		}
	}
	if events[len(events)-1].Type != transcript.DelegatedTaskFailed {
		t.Fatal("missing failed child fact")
	}
}

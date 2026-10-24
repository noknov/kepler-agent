package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/taskstate"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestTaskStateRequiresSourcesAndSurvivesCompaction(t *testing.T) {
	ctx := context.Background()
	store := transcript.NewMemoryStore()
	input := model.TextMessage(model.RoleUser, "Keep the public API compatible")
	source, _ := store.Append(ctx, transcript.Event{SessionID: "s", TurnID: "old", Type: transcript.UserInput, Message: &input})
	item := taskstate.Item{ID: "api", Kind: "constraint", Text: "API compatibility", Status: "active", Sources: []taskstate.Reference{{Sequence: source.Sequence, Quote: "public API compatible"}}}
	execute := func(item taskstate.Item, revision uint64) (tool.Result, error) {
		raw, _ := json.Marshal(taskstate.Patch{ExpectedRevision: revision, Upsert: []taskstate.Item{item}})
		return (taskStateTool{store}).Execute(ctx, tool.Call{Name: "task_state", Scope: tool.Scope{SessionID: "s"}, Arguments: raw})
	}
	bad := item
	bad.Sources = []taskstate.Reference{{Sequence: source.Sequence, Quote: "rewrite everything"}}
	if _, err := execute(bad, 0); err == nil {
		t.Fatal("invented user constraint accepted")
	}
	result, err := execute(item, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result.TaskState)
	saved, _ := store.Append(ctx, transcript.Event{SessionID: "s", Type: transcript.TaskStateUpdated, Metadata: raw})
	summary := model.TextMessage(model.RoleUser, "short summary without the constraint")
	store.Append(ctx, transcript.Event{SessionID: "s", Type: transcript.CompactionCreated, Message: &summary, Metadata: json.RawMessage(`{"covers_through":2}`)})
	if _, err := execute(item, 0); err == nil {
		t.Fatal("stale state revision accepted")
	}
	events, _ := store.Load(ctx, "s", 0)
	window := transcript.TurnWindow(events, "new")
	found := false
	for _, e := range window {
		found = found || e.Sequence == saved.Sequence
	}
	if !found {
		t.Fatal("task state was compacted away")
	}
	projected, err := NewBoundedProjector(ContextConfig{MaxTokens: 2000}).Project(ctx, ProjectRequest{Events: window})
	if err != nil {
		t.Fatal(err)
	}
	var context string
	for _, m := range projected.Messages {
		context += m.Text()
	}
	if !strings.Contains(context, "API compatibility") {
		t.Fatal("structured constraint missing after compaction")
	}
	if result.TaskState.Revision != 1 {
		t.Fatal("unexpected revision")
	}
}

func TestTaskVerificationCannotUseFailureOrPreexistingEvidence(t *testing.T) {
	ctx := context.Background()
	store := transcript.NewMemoryStore()
	message := model.TextMessage(model.RoleUser, "implement and test")
	source, _ := store.Append(ctx, transcript.Event{SessionID: "s", Type: transcript.UserInput, Message: &message})
	call := tool.Call{ID: "test", Name: "test"}
	failed := tool.TextResult("FAIL")
	failed.IsError = true
	failure, _ := store.Append(ctx, transcript.Event{SessionID: "s", Type: transcript.ToolCallFailed, ToolCall: &call, ToolResult: &failed})
	item := taskstate.Item{ID: "test", Kind: "work", Text: "test the change", Status: "verified", Sources: []taskstate.Reference{{Sequence: source.Sequence}}, Evidence: []taskstate.Reference{{Sequence: failure.Sequence}}, Verification: "tests passed"}
	if err := (taskStateTool{store}).validateItem(ctx, "s", item); err == nil {
		t.Fatal("failed test accepted as verification")
	}
	passed := tool.TextResult("PASS")
	success, _ := store.Append(ctx, transcript.Event{SessionID: "s", Type: transcript.ToolCallCompleted, ToolCall: &call, ToolResult: &passed})
	item.Evidence = []taskstate.Reference{{Sequence: success.Sequence, Quote: "PASS"}}
	if err := (taskStateTool{store}).validateItem(ctx, "s", item); err != nil {
		t.Fatal(err)
	}
	if err := (taskStateTool{store}).validateItem(ctx, "other", item); err == nil {
		t.Fatal("cross-session evidence accepted")
	}
}

func TestTaskStateToolPersistsThroughRuntime(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	store := transcript.NewMemoryStore()
	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "state", Name: "task_state", Arguments: json.RawMessage(`{"expected_revision":0,"upsert":[{"id":"goal","kind":"goal","text":"keep compatibility","status":"active","sources":[{"sequence":4,"quote":"compatible"}]}]}`)}}}}, FinishReason: model.FinishToolCalls},
		{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop},
	}}
	runner, err := New(Config{}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	catalog.Activate("s", "task_state")
	// SessionStarted, SessionInitialized, and TurnStarted precede fresh input.
	_, err = runner.RunTurn(context.Background(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "keep it compatible")})
	if err != nil {
		t.Fatal(err)
	}
	events, _ := store.Load(context.Background(), "s", 0)
	state, err := latestTaskState(events)
	if err != nil || state.Revision != 1 {
		t.Fatalf("state=%+v err=%v events=%+v", state, err, events)
	}
}

func TestUnknownEffectIsRuntimeOwnedAndRetainedUntilReconciled(t *testing.T) {
	ctx := context.Background()
	store := transcript.NewMemoryStore()
	catalog, _ := tool.NewCatalog()
	runner, err := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	result := tool.TextResult("outcome unknown")
	result.IsError = true
	result.ErrorCode = "execution_interrupted"
	call := tool.Call{ID: "write", Name: "external_write"}
	failure, _ := store.Append(ctx, transcript.Event{SessionID: "s", TurnID: "t", Type: transcript.ToolCallFailed, ToolCall: &call, ToolResult: &result})
	if err := runner.captureUnknownEffect(ctx, failure); err != nil {
		t.Fatal(err)
	}
	if err := runner.captureUnknownEffect(ctx, failure); err != nil {
		t.Fatal(err)
	}
	events, _ := store.Load(ctx, "s", 0)
	state, err := latestTaskState(events)
	if err != nil || len(state.Items) != 1 || state.Items[0].Status != "unknown" || state.Revision != 1 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	item := state.Items[0]
	item.Status = "done"
	if _, err := taskstate.Apply(state, taskstate.Patch{ExpectedRevision: 1, Upsert: []taskstate.Item{item}}); err == nil {
		t.Fatal("unknown effect marked done without reconciliation")
	}
	item.Status = "reconciled"
	item.Verification = "checked external service"
	item.Evidence = []taskstate.Reference{{Sequence: failure.Sequence}}
	if err := (taskStateTool{store}).validateItem(ctx, "s", item); err == nil {
		t.Fatal("unknown result used as its own verification")
	}
}

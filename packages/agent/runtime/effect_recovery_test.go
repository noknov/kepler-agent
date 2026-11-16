package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/taskstate"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type uncertainEffectFailure struct{}

func (uncertainEffectFailure) Error() string         { return "response lost" }
func (uncertainEffectFailure) EffectUncertain() bool { return true }

func TestLiveUncertainEffectSurvivesCanonicalAndModelProjection(t *testing.T) {
	catalog, _ := tool.NewCatalog(auditTool{name: "write", execute: func(context.Context) (tool.Result, error) { return tool.Result{}, uncertainEffectFailure{} }})
	call := model.ToolCall{ID: "call", Name: "write", Arguments: json.RawMessage(`{}`)}
	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}}, FinishReason: model.FinishToolCalls},
		{Message: model.TextMessage(model.RoleAssistant, "verify before retrying"), FinishReason: model.FinishStop},
	}}
	store := transcript.NewMemoryStore()
	runner, _ := New(Config{}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if _, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "write")}); err != nil {
		t.Fatal(err)
	}
	events, _ := store.Load(t.Context(), "s", 0)
	found := false
	for _, e := range events {
		if e.Type == transcript.ToolCallFailed {
			found = e.ToolResult.OutcomeUnknown
		}
	}
	state, err := latestTaskState(events)
	if !found || err != nil || len(state.Items) != 1 || state.Items[0].Status != "unknown" {
		t.Fatalf("unknown effect lost: receipt=%t state=%+v err=%v", found, state, err)
	}
	visible := false
	for _, m := range client.requests[1].Messages {
		for _, b := range m.Content {
			if b.ToolResult != nil {
				for _, c := range b.ToolResult.Content {
					var status struct {
						Unknown bool `json:"outcome_unknown"`
					}
					if c.Type == model.ContentJSON && json.Unmarshal(c.JSON, &status) == nil {
						visible = visible || status.Unknown
					}
				}
			}
		}
	}
	if !visible {
		t.Fatal("model cannot distinguish an unknown effect from a known rejection")
	}
	summary := model.TextMessage(model.RoleUser, "checkpoint without the write details")
	raw, _ := json.Marshal(map[string]any{"covers_through": events[len(events)-1].Sequence})
	if _, err := store.Append(t.Context(), transcript.Event{SessionID: "s", Type: transcript.CompactionCreated, Message: &summary, Metadata: raw}); err != nil {
		t.Fatal(err)
	}
	events, _ = store.Load(t.Context(), "s", 0)
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 4000}).Project(t.Context(), ProjectRequest{Events: transcript.TurnWindow(events, "new")})
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, message := range projection.Messages {
		text += message.Text()
	}
	if !strings.Contains(text, "Outcome unknown for tool write") {
		t.Fatal("checkpoint erased the unknown external effect")
	}

}

func TestInterruptedApprovalResolutionUsesDurableStartEvidence(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "not_started", true: "started"}[started], func(t *testing.T) {
			writes := 0
			item := countingWriteTool{calls: &writes}
			catalog, _ := tool.NewCatalog(item)
			store := transcript.NewMemoryStore()
			call := tool.Call{ID: "call", ExecutionID: "turn:call", Name: "write", Arguments: json.RawMessage(`{}`), Scope: tool.Scope{UserID: "U1", SessionID: "s", TurnID: "turn"}}
			meta, _ := json.Marshal(approvalRequestMetadata{Descriptor: ptrDescriptor(item.Descriptor())})
			for _, e := range []transcript.Event{
				{SessionID: "s", TurnID: "turn", Type: transcript.ApprovalRequested, ToolCall: &call, Metadata: meta},
				{SessionID: "s", TurnID: "turn", Type: transcript.ApprovalResolved, ToolCall: &call, Metadata: json.RawMessage(`{"approved":true,"resolved_by":"U1"}`)},
			} {
				if _, err := store.Append(t.Context(), e); err != nil {
					t.Fatal(err)
				}
			}
			if started {
				store.Append(t.Context(), transcript.Event{SessionID: "s", TurnID: "turn", Type: transcript.ToolCallStarted, ToolCall: &call})
			}
			runner, _ := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: store, Policy: approvalPolicy{}})
			resolution := ApprovalResolution{TurnID: "turn", ToolCallID: "call", UserID: "U1", Approved: true}
			if err := runner.ResolveApproval(t.Context(), "s", resolution); err != nil {
				t.Fatal(err)
			}
			if err := runner.ResolveApproval(t.Context(), "s", resolution); err != nil {
				t.Fatal(err)
			}
			want := 1
			if started {
				want = 0
			}
			if writes != want {
				t.Fatalf("writes=%d want=%d", writes, want)
			}
			events, _ := store.Load(t.Context(), "s", 0)
			found := false
			for _, e := range events {
				if e.ToolResult != nil {
					found = true
					if e.ToolResult.OutcomeUnknown != started {
						t.Fatalf("outcome=%+v", e.ToolResult)
					}
				}
			}
			if !found {
				t.Fatal("resolution has no durable result")
			}
		})
	}
}
func ptrDescriptor(d tool.Descriptor) *tool.Descriptor { return &d }

func TestEffectRepairDoesNotResurrectArchivedResolvedItems(t *testing.T) {
	store := transcript.NewMemoryStore()
	catalog, _ := tool.NewCatalog()
	runner, _ := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: store})
	call := tool.Call{ID: "call", Name: "write"}
	result := interruptedToolResult(call)
	receipt, err := store.Append(t.Context(), transcript.Event{SessionID: "s", TurnID: "t", Type: transcript.ToolCallFailed, ToolCall: &call, ToolResult: &result})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.captureUnknownEffect(t.Context(), receipt); err != nil {
		t.Fatal(err)
	}
	events, _ := store.Load(t.Context(), "s", 0)
	state, err := latestTaskState(events)
	if err != nil {
		t.Fatal(err)
	}
	state.Items[0].Status = "reconciled"
	// A successful read and model patch would first reconcile the item; archival
	// then retains the runtime's explicit observation cursor, not an inferred timer.
	patch := taskstate.Patch{ExpectedRevision: state.Revision, Archive: []string{state.Items[0].ID}}
	state, err = taskstate.Apply(state, patch)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(state)
	store.Append(t.Context(), transcript.Event{SessionID: "s", Type: transcript.TaskStateUpdated, Metadata: raw})
	if err := runner.captureUnknownEffect(t.Context(), receipt); err != nil {
		t.Fatal(err)
	}
	events, _ = store.Load(t.Context(), "s", 0)
	state, err = latestTaskState(events)
	if err != nil || len(state.Items) != 0 || state.EffectsThrough != receipt.Sequence {
		t.Fatalf("archived effect returned: %+v %v", state, err)
	}
}

type orderedContinuationReceipt struct{ saved bool }

func (s *orderedContinuationReceipt) Save(context.Context, ConnectionContinuation) error {
	s.saved = true
	return nil
}

type orderedWaitingStore struct {
	transcript.Store
	dependency *orderedContinuationReceipt
	observed   bool
}

func (s *orderedWaitingStore) Append(ctx context.Context, e transcript.Event) (transcript.Event, error) {
	if e.Type == transcript.ToolCallFailed && e.ToolResult.ErrorCode == "connection_required" {
		s.observed = s.dependency.saved
		if !s.observed {
			return transcript.Event{}, fmt.Errorf("waiting receipt preceded its continuation")
		}
	}
	return s.Store.Append(ctx, e)
}
func TestWaitingReceiptCannotPrecedeDurableContinuation(t *testing.T) {
	dependency := &orderedContinuationReceipt{}
	store := &orderedWaitingStore{Store: transcript.NewMemoryStore(), dependency: dependency}
	catalog, _ := tool.NewCatalog(auditTool{name: "connect", execute: func(context.Context) (tool.Result, error) {
		return tool.Result{IsError: true, ErrorCode: "connection_required", NeedsUserInput: true, Content: tool.TextResult("Connect first").Content, Metadata: map[string]any{"provider": "github", "instance_id": "default"}}, nil
	}})
	call := model.ToolCall{ID: "call", Name: "connect", Arguments: json.RawMessage(`{}`)}
	client := &scriptedModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}}, FinishReason: model.FinishToolCalls}}}
	runner, _ := New(Config{}, Dependencies{Model: client, Tools: catalog, Transcript: store, ConnectionContinuations: dependency})
	result, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "connect"), Scope: tool.Scope{UserID: "U1"}})
	if err != nil || result.Termination != TerminationPendingInput || !store.observed {
		t.Fatalf("result=%+v err=%v dependency_saved=%t", result, err, store.observed)
	}
}

type interruptedLedgerStore struct {
	transcript.Store
	failed bool
}

func (s *interruptedLedgerStore) Append(ctx context.Context, e transcript.Event) (transcript.Event, error) {
	if e.Type == transcript.TaskStateUpdated && !s.failed {
		s.failed = true
		return transcript.Event{}, fmt.Errorf("ledger commit interrupted")
	}
	return s.Store.Append(ctx, e)
}
func TestUnknownEffectLedgerRepairsAfterReceiptCommitAndRestart(t *testing.T) {
	memory := transcript.NewMemoryStore()
	store := &interruptedLedgerStore{Store: memory}
	writes := 0
	catalog, _ := tool.NewCatalog(auditTool{name: "write", execute: func(context.Context) (tool.Result, error) { writes++; return tool.Result{}, uncertainEffectFailure{} }})
	call := model.ToolCall{ID: "call", Name: "write", Arguments: json.RawMessage(`{}`)}
	first := &scriptedModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}}, FinishReason: model.FinishToolCalls}}}
	runner, _ := New(Config{}, Dependencies{Model: first, Tools: catalog, Transcript: store})
	if _, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "old", Input: model.TextMessage(model.RoleUser, "write")}); err == nil {
		t.Fatal("ledger failure was hidden")
	}
	restarted, _ := New(Config{}, Dependencies{Model: &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "inspect the previous write"), FinishReason: model.FinishStop}}}, Tools: catalog, Transcript: memory})
	if _, err := restarted.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "new", Input: model.TextMessage(model.RoleUser, "continue")}); err != nil {
		t.Fatal(err)
	}
	events, _ := memory.Load(t.Context(), "s", 0)
	state, err := latestTaskState(events)
	if err != nil || writes != 1 || len(state.Items) != 1 || state.Items[0].Status != "unknown" {
		t.Fatalf("writes=%d state=%+v err=%v", writes, state, err)
	}
}

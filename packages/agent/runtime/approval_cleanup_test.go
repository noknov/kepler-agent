package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type approvalResourceTool struct {
	cancel context.CancelFunc
	fail   bool
	open   bool
	calls  int
	closed int
}

func (*approvalResourceTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("scratch_write", "fixture", tool.ObjectSchema(nil, nil), tool.WithEffects(tool.EffectExternalWrite))
}

func (t *approvalResourceTool) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	t.calls++
	t.open = true
	tool.CacheFor(call.Scope).Set("private-scratch", true)
	if t.cancel != nil {
		t.cancel()
	}
	if t.fail {
		return tool.Result{}, errors.New("write failed")
	}
	return tool.TextResult("wrote"), nil
}

func (t *approvalResourceTool) EndTurn(string, string) {
	if t.open {
		t.closed++
		t.open = false
	}
}

type contextCheckingApprovalStore struct{ transcript.Store }

func (s contextCheckingApprovalStore) Append(ctx context.Context, event transcript.Event) (transcript.Event, error) {
	if err := ctx.Err(); err != nil {
		return transcript.Event{}, err
	}
	return s.Store.Append(ctx, event)
}

func TestApprovalResolutionFinalizesToolResourcesAndReceipts(t *testing.T) {
	for _, mode := range []string{"success", "tool failure", "canceled after write", "blocked receipt"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			item := &approvalResourceTool{fail: mode == "tool failure"}
			if mode == "canceled after write" {
				item.cancel = cancel
			}
			memory := transcript.NewMemoryStore()
			var store transcript.Store = contextCheckingApprovalStore{Store: memory}
			if mode == "blocked receipt" {
				store = &cleanupProbeStore{Store: store, target: transcript.ToolCallCompleted}
			}
			catalog, _ := tool.NewCatalog(item)
			client := &scriptedModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call", Name: "scratch_write", Arguments: json.RawMessage(`{}`)}}}}, FinishReason: model.FinishToolCalls}}}
			runner, err := New(Config{CleanupTimeout: 20 * time.Millisecond}, Dependencies{Model: client, Tools: catalog, Transcript: store, Policy: approvalPolicy{}})
			if err != nil {
				t.Fatal(err)
			}
			sessionID := t.Name()
			t.Cleanup(func() { tool.ClearTurnCache(sessionID, "turn") })
			result, err := runner.RunTurn(ctx, TurnRequest{SessionID: sessionID, TurnID: "turn", Input: model.TextMessage(model.RoleUser, "write"), Scope: tool.Scope{UserID: "user"}})
			if err != nil || result.Termination != TerminationPendingApproval || item.calls != 0 {
				t.Fatalf("approval request=%+v err=%v calls=%d", result, err, item.calls)
			}
			resolution := ApprovalResolution{TurnID: "turn", ToolCallID: "call", UserID: "user", Approved: true}
			err = runner.ResolveApproval(ctx, sessionID, resolution)
			if mode == "blocked receipt" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("receipt cleanup was not bounded: %v", err)
				}
			} else if err != nil {
				t.Fatalf("tool receipt was lost: %v", err)
			}
			_, scratch := tool.CacheFor(tool.Scope{SessionID: sessionID, TurnID: "turn"}).Get("private-scratch")
			runner.circuitMu.Lock()
			_, circuit := runner.circuits[sessionID]
			runner.circuitMu.Unlock()
			if item.open || item.closed != 1 || scratch || circuit {
				t.Fatalf("approval resources survived: open=%v closed=%d scratch=%v circuit=%v", item.open, item.closed, scratch, circuit)
			}
			duplicateErr := runner.ResolveApproval(t.Context(), sessionID, resolution)
			if (duplicateErr != nil) != (mode == "blocked receipt") || item.calls != 1 {
				t.Fatalf("duplicate resolution=%v calls=%d", duplicateErr, item.calls)
			}
		})
	}
}

package luckin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type orderModel struct{ calls []string }

func (m *orderModel) Generate(context.Context, model.Request, model.EventSink) (model.Response, error) {
	name := m.calls[0]
	m.calls = m.calls[1:]
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{
		Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: name, Name: name, Arguments: json.RawMessage(`{"deptId":101,"productList":[{"productId":202,"skuCode":"sku","amount":1}]}`)},
	}}}, FinishReason: model.FinishToolCalls}, nil
}

type confirmOrderPolicy struct{}

func (confirmOrderPolicy) Decide(_ context.Context, request tool.PolicyRequest) (tool.Decision, error) {
	for _, effect := range request.Descriptor.Effects {
		if effect == tool.EffectExternalWrite {
			return tool.Decision{Type: tool.DecisionRequireApproval}, nil
		}
	}
	return tool.Decision{Type: tool.DecisionAllow}, nil
}

func TestPreviewSurvivesDeferredApproval(t *testing.T) {
	for _, test := range []struct {
		name        string
		restart     bool
		approve     bool
		rotateToken bool
	}{
		{"same worker", false, true, false},
		{"new worker", true, true, false},
		{"declined", true, false, false},
		{"credential rotated", true, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			client := resultClient(t, `{"content":[{"type":"text","text":"ok"}]}`, &calls)
			catalogFor := func() *tool.Catalog {
				var items []tool.Tool
				for _, item := range Tools(client) {
					items = append(items, tool.BindSurface(item, "slack"))
				}
				catalog, err := tool.NewCatalog(items...)
				if err != nil {
					t.Fatal(err)
				}
				if err := catalog.SetPromptOverlay(func(d tool.Descriptor) tool.Descriptor { return d }); err != nil {
					t.Fatal(err)
				}
				return catalog
			}
			store := transcript.NewMemoryStore()
			newRunner := func(catalog *tool.Catalog) *runtime.Runtime {
				runner, err := runtime.New(runtime.Config{}, runtime.Dependencies{
					Model: &orderModel{calls: []string{"luckin-preview_order", "luckin-create_order"}},
					Tools: catalog, Policy: confirmOrderPolicy{}, Transcript: store,
				})
				if err != nil {
					t.Fatal(err)
				}
				return runner
			}
			runner := newRunner(catalogFor())
			session := t.Name()
			result, err := runner.RunTurn(t.Context(), runtime.TurnRequest{
				SessionID: session, TurnID: "turn", Scope: tool.Scope{UserID: "user"}, Input: model.TextMessage(model.RoleUser, "order"),
			})
			if err != nil || result.Termination != runtime.TerminationPendingApproval || len(calls) != 1 {
				t.Fatalf("approval did not suspend after preview: result=%+v calls=%v err=%v", result, calls, err)
			}
			if test.restart {
				events, err := store.Load(t.Context(), session, 0)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(events)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &events); err != nil {
					t.Fatal(err)
				}
				store = transcript.NewMemoryStore()
				if _, err := store.AppendBatch(t.Context(), events); err != nil {
					t.Fatal(err)
				}
				client = resultClient(t, `{"content":[{"type":"text","text":"ok"}]}`, &calls)
				if test.rotateToken {
					client.MCP.Token = "different-account-token"
				}
				runner = newRunner(catalogFor())
			}
			resolution := runtime.ApprovalResolution{TurnID: "turn", ToolCallID: "luckin-create_order", UserID: "user", Approved: test.approve}
			err = runner.ResolveApproval(t.Context(), session, resolution)
			if test.rotateToken {
				if err == nil || len(calls) != 1 {
					t.Fatalf("credential change reused approval: calls=%v err=%v", calls, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if test.approve {
				want = 2
			}
			if len(calls) != want || (test.approve && calls[1] != "createOrder") {
				t.Fatalf("successful preview was lost across approval: calls=%v", calls)
			}
			if err := runner.ResolveApproval(t.Context(), session, resolution); err != nil || len(calls) != want {
				t.Fatalf("duplicate resolution repeated order: calls=%v err=%v", calls, err)
			}
		})
	}
}

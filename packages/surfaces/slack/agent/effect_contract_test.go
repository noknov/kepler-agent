package slackagent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

type effectRoundTrip func(*http.Request) (*http.Response, error)

func (f effectRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type slackEffectTool struct{ client *slack.Client }

func (slackEffectTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("native_write", "fixture", tool.ObjectSchema(nil, nil), tool.WithEffects(tool.EffectExternalWrite))
}
func (t slackEffectTool) Execute(ctx context.Context, _ tool.Call) (tool.Result, error) {
	return tool.TextResult("updated"), t.client.SetCodeContext(ctx, "C1", nil)
}

type effectPolicy struct{}

func (effectPolicy) Decide(context.Context, tool.PolicyRequest) (tool.Decision, error) {
	return tool.Decision{Type: tool.DecisionAllow}, nil
}

type effectModel struct {
	calls   int
	request model.Request
}

func (m *effectModel) Generate(_ context.Context, request model.Request, _ model.EventSink) (model.Response, error) {
	m.calls++
	m.request = request
	if m.calls == 1 {
		call := model.ToolCall{ID: "call", Name: "native_write", Arguments: json.RawMessage(`{}`)}
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}}, FinishReason: model.FinishToolCalls}, nil
	}
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "verify the effect"), FinishReason: model.FinishStop}, nil
}
func TestSlackUncertainWriteReachesCanonicalEffectLedger(t *testing.T) {
	calls := 0
	client := slack.NewTestClient(effectRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unavailable")), Request: r}, nil
	}))
	catalog, _ := tool.NewCatalog(slackEffectTool{client})
	store := transcript.NewMemoryStore()
	modelClient := &effectModel{}
	runner, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: modelClient, Tools: catalog, Transcript: store, Policy: effectPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTurn(t.Context(), agentruntime.TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "update")}); err != nil {
		t.Fatal(err)
	}
	events, _ := store.Load(t.Context(), "s", 0)
	unknown, ledger := false, false
	for _, event := range events {
		if event.Type == transcript.ToolCallFailed {
			unknown = event.ToolResult.OutcomeUnknown
		}
		if event.Type == transcript.TaskStateUpdated {
			ledger = strings.Contains(string(event.Metadata), "Outcome unknown")
		}
	}
	if calls != 1 || !unknown || !ledger {
		t.Fatalf("calls=%d unknown=%t ledger=%t", calls, unknown, ledger)
	}
}

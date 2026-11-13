package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type measuredContextClient struct {
	*scriptedModel
	route string
}

func (c *measuredContextClient) InputContextKey(context.Context, string) (string, error) {
	return c.route, nil
}

func TestReportedContextIsReusedOnlyForItsExactRequestPrefix(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	store := transcript.NewMemoryStore()
	client := &measuredContextClient{route: "primary-route", scriptedModel: &scriptedModel{responses: []model.Response{{
		InputContextKey: "primary-route", Message: model.TextMessage(model.RoleAssistant, "answer"), FinishReason: model.FinishStop,
		Usage: model.Usage{InputTokens: 700, CacheReadTokens: 300},
	}}}}
	runner, err := New(Config{Model: "m", Context: ContextConfig{MaxTokens: 20000}}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	turn := TurnRequest{SessionID: "session", TurnID: "turn"}
	messages := []model.Message{model.TextMessage(model.RoleUser, "original prefix")}
	runner.setEventCache(turn.SessionID, nil)
	defer runner.clearEventCache(turn.SessionID)
	if _, err := runner.generateWithTools(t.Context(), turn, messages, nil); err != nil {
		t.Fatal(err)
	}
	tail := model.TextMessage(model.RoleAssistant, "new content")
	continued := append(append([]model.Message(nil), messages...), tail)
	want := 1000 + runner.config.Context.counter().Messages([]model.Message{tail})
	if got := runner.inputEstimate(t.Context(), turn, continued, nil); got != want {
		t.Fatalf("reported cached input was not retained: got=%d want=%d", got, want)
	}
	for _, test := range []struct {
		name string
		edit func()
	}{
		{"prompt edit", func() { continued[0] = model.TextMessage(model.RoleUser, "edited prefix") }},
		{"route change", func() { client.route = "another-route" }},
		{"model change", func() { turn.Model = "another-model" }},
		{"reasoning change", func() { runner.config.ReasoningEffort = "high" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			saved := append([]model.Message(nil), continued...)
			test.edit()
			fallback := runner.config.Context.counter().Messages(continued)
			if got := runner.inputEstimate(t.Context(), turn, continued, nil); got != fallback {
				t.Fatalf("stale observation survived %s: got=%d want=%d", test.name, got, fallback)
			}
			continued = saved
			client.route = "primary-route"
			turn.Model = ""
			runner.config.ReasoningEffort = ""
		})
	}
	definitions := []model.ToolDefinition{{Name: "new", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	if got, expected := runner.inputEstimate(t.Context(), turn, continued, definitions), runner.config.Context.counter().Messages(continued)+runner.config.Context.counter().Tools(definitions); got != expected {
		t.Fatalf("changed schemas retained stale usage: got=%d want=%d", got, expected)
	}
	// Reconstruct from durable facts, as a replacement process does.
	events, err := store.Load(t.Context(), turn.SessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	runner.setEventCache(turn.SessionID, events)
	if got := runner.inputEstimate(t.Context(), turn, continued, nil); got != want {
		t.Fatalf("restored observation changed: got=%d want=%d", got, want)
	}
	// A successful response without a usable measurement replaces the old one.
	_, err = runner.record(context.Background(), transcript.Event{SessionID: turn.SessionID, TurnID: turn.TurnID, Type: transcript.ModelCompleted, Metadata: json.RawMessage(`{"usage":{}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got, expected := runner.inputEstimate(t.Context(), turn, continued, nil), runner.config.Context.counter().Messages(continued); got != expected {
		t.Fatalf("missing usage did not invalidate observation: got=%d want=%d", got, expected)
	}
}

func TestIncompleteOrUnknownRouteDoesNotProduceUsageAnchor(t *testing.T) {
	runner := &Runtime{}
	request := model.Request{Model: "m", Messages: []model.Message{model.TextMessage(model.RoleUser, "input")}}
	response := model.Response{InputContextKey: "route", FinishReason: model.FinishCanceled, Usage: model.Usage{InputTokens: 100}}
	if runner.observeContext(request, response) != nil {
		t.Fatal("incomplete request produced an anchor")
	}
	response.InputContextKey = ""
	if runner.observeContext(request, response) != nil {
		t.Fatal("unknown route produced an anchor")
	}
}

func TestReportedUsageTriggersCompactionBeforeAnotherModelCall(t *testing.T) {
	store := transcript.NewMemoryStore()
	for _, message := range []model.Message{
		model.TextMessage(model.RoleUser, "Earlier requirement: preserve the old API."),
		model.TextMessage(model.RoleAssistant, "The old API has been inspected."),
	} {
		kind := transcript.UserInput
		if message.Role == model.RoleAssistant {
			kind = transcript.AssistantMessage
		}
		if _, err := store.Append(t.Context(), transcript.Event{SessionID: "session", TurnID: "old", Type: kind, Message: &message}); err != nil {
			t.Fatal(err)
		}
	}
	client := &measuredContextClient{route: "primary-route", scriptedModel: &scriptedModel{responses: []model.Response{
		{
			InputContextKey: "primary-route", FinishReason: model.FinishToolCalls,
			Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call", Name: "echo", Arguments: json.RawMessage(`{"value":"evidence"}`)}}}},
			Usage:   model.Usage{InputTokens: 5900},
		},
		{InputContextKey: "primary-route", FinishReason: model.FinishStop, Message: model.TextMessage(model.RoleAssistant, "done"), Usage: model.Usage{InputTokens: 100}},
	}}}
	catalog, _ := tool.NewCatalog(echoTool{})
	compactor := &recordingCompactor{}
	runner, err := New(Config{Model: "m", Context: ContextConfig{MaxTokens: 12000, ReserveTokens: 5000, OutputTokens: 1000}}, Dependencies{Model: client, Tools: catalog, Transcript: store, Compactor: compactor})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "session", TurnID: "turn", Input: model.TextMessage(model.RoleUser, "Continue the investigation.")}); err != nil {
		t.Fatal(err)
	}
	if !compactor.called || len(client.requests) != 2 {
		t.Fatalf("reported input did not drive admission: compacted=%t calls=%d", compactor.called, len(client.requests))
	}
}

func BenchmarkInputContextEstimate(b *testing.B) {
	for _, observed := range []bool{false, true} {
		name := "estimated"
		if observed {
			name = "observed_prefix"
		}
		b.Run(name, func(b *testing.B) {
			messages := make([]model.Message, 200)
			for i := range messages {
				messages[i] = model.TextMessage(model.RoleUser, strings.Repeat("source evidence ", 100))
			}
			request := model.Request{Model: "m", Messages: messages}
			client := &measuredContextClient{route: "route"}
			runner := &Runtime{
				config: Config{Model: "m"}, deps: Dependencies{Model: client},
				eventCache: make(map[string]*sessionEvents),
			}
			want := runner.config.Context.counter().Messages(messages)
			state := &sessionEvents{}
			if observed {
				state.contextUsage = runner.observeContext(request, model.Response{InputContextKey: "route", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: int64(want)}})
			}
			runner.eventCache["session"] = state
			turn := TurnRequest{SessionID: "session"}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got := runner.inputEstimate(b.Context(), turn, messages, nil); got != want {
					b.Fatalf("input estimate=%d want=%d", got, want)
				}
			}
		})
	}
}

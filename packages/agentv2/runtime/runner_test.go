package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/prompt"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/transcript"
)

type scriptedModel struct {
	mu        sync.Mutex
	responses []model.Response
	errors    []error
	requests  []model.Request
}

func (s *scriptedModel) Generate(_ context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	index := len(s.requests) - 1
	if index < len(s.errors) && s.errors[index] != nil {
		return model.Response{}, s.errors[index]
	}
	response := s.responses[index]
	if sink != nil {
		_ = sink(model.StreamEvent{Type: model.StreamCompleted, Usage: &response.Usage})
	}
	return response, nil
}

type echoTool struct{}

func (echoTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`), Parallel: true}
}
func (echoTool) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	return tool.TextResult(string(call.Arguments)), nil
}

func TestRunTurnExecutesToolAndRecordsCanonicalHistory(t *testing.T) {
	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call-1", Name: "echo", Arguments: json.RawMessage(`{"value":"hi"}`)}}}}, FinishReason: model.FinishToolCalls},
		{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 10, OutputTokens: 2}},
	}}
	catalog, err := tool.NewCatalog(echoTool{})
	if err != nil {
		t.Fatal(err)
	}
	store := transcript.NewMemoryStore()
	runner, err := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(context.Background(), TurnRequest{
		SessionID: "s1", Input: model.TextMessage(model.RoleUser, "echo hi"),
		Prompt: []prompt.Fragment{{ID: "core", Layer: prompt.LayerCore, Content: "be useful"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Termination != TerminationCompleted || result.Message.Text() != "done" {
		t.Fatalf("result = %#v", result)
	}
	if len(client.requests) != 2 {
		t.Fatalf("model requests = %d", len(client.requests))
	}
	lastMessages := client.requests[1].Messages
	if len(lastMessages) < 4 || lastMessages[len(lastMessages)-1].Role != model.RoleTool {
		t.Fatalf("messages = %#v", lastMessages)
	}
	events, err := store.Load(context.Background(), "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawCall, sawResult bool
	for _, event := range events {
		sawCall = sawCall || event.Type == transcript.ToolCallStarted
		sawResult = sawResult || event.Type == transcript.ToolCallCompleted
	}
	if !sawCall || !sawResult {
		t.Fatalf("missing tool lifecycle events: %#v", events)
	}
}

func TestRunTurnRetriesTransientModelError(t *testing.T) {
	client := &scriptedModel{
		errors:    []error{&model.Error{Kind: model.ErrorTransient, Message: "retry", Retryable: true}, nil},
		responses: []model.Response{{}, {Message: model.TextMessage(model.RoleAssistant, "ok"), FinishReason: model.FinishStop}},
	}
	catalog, _ := tool.NewCatalog()
	runner, err := New(Config{Model: "test", RetryBaseDelay: time.Nanosecond}, Dependencies{
		Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore(),
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "s2", Input: model.TextMessage(model.RoleUser, "hi")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message.Text() != "ok" || len(client.requests) != 2 {
		t.Fatalf("result=%#v requests=%d", result, len(client.requests))
	}
}

type denyPolicy struct{}

func (denyPolicy) Decide(context.Context, tool.PolicyRequest) (tool.Decision, error) {
	return tool.Decision{Type: tool.DecisionDeny, Reason: "test"}, nil
}

func TestRunTurnFeedsPolicyDenialBackToModel(t *testing.T) {
	call := model.Content{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "c", Name: "echo", Arguments: json.RawMessage(`{}`)}}
	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{call}}, FinishReason: model.FinishToolCalls},
		{Message: model.TextMessage(model.RoleAssistant, "cannot run"), FinishReason: model.FinishStop},
	}}
	catalog, _ := tool.NewCatalog(echoTool{})
	runner, err := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Policy: denyPolicy{}, Transcript: transcript.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "s3", Input: model.TextMessage(model.RoleUser, "run")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message.Text() != "cannot run" {
		t.Fatalf("message = %q", result.Message.Text())
	}
}

func TestRunTurnReturnsModelErrorAfterRetryBudget(t *testing.T) {
	client := &scriptedModel{errors: []error{errors.New("boom")}}
	catalog, _ := tool.NewCatalog()
	runner, err := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "s4", Input: model.TextMessage(model.RoleUser, "hi")})
	if err == nil || result.Termination != TerminationModelError {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/prompt"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
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
	return tool.Descriptor{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []tool.Effect{tool.EffectRead}, Parallel: true}
}

type askTool struct{}

func (askTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "ask", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []tool.Effect{tool.EffectRead}, Exclusive: true}
}

type blockingModel struct{}

type panicTool struct{}

func (panicTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "panic", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []tool.Effect{tool.EffectRead}, Parallel: true}
}
func (panicTool) Execute(context.Context, tool.Call) (tool.Result, error) { panic("boom") }

type countingWriteTool struct{ calls *int }

func (t countingWriteTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "write", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []tool.Effect{tool.EffectExternalWrite}}
}
func (t countingWriteTool) Execute(context.Context, tool.Call) (tool.Result, error) {
	*t.calls++
	return tool.TextResult("wrote"), nil
}

func (blockingModel) Generate(ctx context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	<-ctx.Done()
	return model.Response{}, ctx.Err()
}

type parallelProbeTool struct {
	name    string
	started chan<- string
	release <-chan struct{}
}

func (t parallelProbeTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: t.name, InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []tool.Effect{tool.EffectRead}, Parallel: true}
}
func (t parallelProbeTool) Execute(context.Context, tool.Call) (tool.Result, error) {
	t.started <- t.name
	<-t.release
	return tool.TextResult("ok"), nil
}

type recordingCompactor struct{ called bool }

func (c *recordingCompactor) Compact(_ context.Context, _ []model.Message, _ int) (model.Message, error) {
	c.called = true
	return model.TextMessage(model.RoleSystem, "compacted"), nil
}
func (askTool) Execute(context.Context, tool.Call) (tool.Result, error) {
	result := tool.TextResult("Which environment should I use?")
	result.NeedsUserInput = true
	return result, nil
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
	runner, err := New(Config{Model: "test", MaxModelRetries: 1, RetryBaseDelay: time.Nanosecond}, Dependencies{
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

func TestRunTurnZeroRetryBudgetDoesNotRetry(t *testing.T) {
	client := &scriptedModel{errors: []error{&model.Error{Kind: model.ErrorTransient, Message: "retryable", Retryable: true}}}
	catalog, _ := tool.NewCatalog()
	runner, err := New(Config{Model: "test", MaxModelRetries: 0}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunTurn(context.Background(), TurnRequest{SessionID: "zero-retry", Input: model.TextMessage(model.RoleUser, "hi")})
	if err == nil || len(client.requests) != 1 {
		t.Fatalf("err=%v requests=%d, want one attempt", err, len(client.requests))
	}
}

func TestRunTurnRejectsEmptyModelResponse(t *testing.T) {
	client := &scriptedModel{responses: []model.Response{{}}}
	catalog, _ := tool.NewCatalog()
	runner, _ := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	result, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "empty-retry", Input: model.TextMessage(model.RoleUser, "hi")})
	if err == nil || result.Termination != TerminationEmptyResponse || len(client.requests) != 1 {
		t.Fatalf("result=%+v requests=%d err=%v", result, len(client.requests), err)
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

func TestRunTurnStopsWhenToolNeedsUserInput(t *testing.T) {
	client := &scriptedModel{responses: []model.Response{{
		Message:      model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "ask-1", Name: "ask", Arguments: json.RawMessage(`{}`)}}}},
		FinishReason: model.FinishToolCalls,
	}}}
	catalog, _ := tool.NewCatalog(askTool{})
	store := transcript.NewMemoryStore()
	runner, err := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "pending", Input: model.TextMessage(model.RoleUser, "deploy")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Termination != TerminationPendingInput || result.Message.Text() != "Which environment should I use?" || len(client.requests) != 1 {
		t.Fatalf("result=%+v requests=%d", result, len(client.requests))
	}
}

func TestRunTurnRejectsMixedExclusiveToolBatchWithoutSideEffects(t *testing.T) {
	call := func(id, name string) model.Content {
		return model.Content{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(`{}`)}}
	}
	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{call("ask-1", "ask"), call("write-1", "write")}}, FinishReason: model.FinishToolCalls},
		{Message: model.TextMessage(model.RoleAssistant, "I need to ask separately."), FinishReason: model.FinishStop},
	}}
	writes := 0
	catalog, _ := tool.NewCatalog(askTool{}, countingWriteTool{calls: &writes})
	runner, _ := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	result, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "exclusive", Input: model.TextMessage(model.RoleUser, "deploy")})
	if err != nil || writes != 0 || result.Message.Text() != "I need to ask separately." {
		t.Fatalf("result=%+v writes=%d err=%v", result, writes, err)
	}
}

func TestParallelToolPanicBecomesToolFailure(t *testing.T) {
	call := model.Content{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "panic-1", Name: "panic", Arguments: json.RawMessage(`{}`)}}
	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{call}}, FinishReason: model.FinishToolCalls},
		{Message: model.TextMessage(model.RoleAssistant, "recovered"), FinishReason: model.FinishStop},
	}}
	catalog, _ := tool.NewCatalog(panicTool{})
	runner, _ := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	result, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "panic", Input: model.TextMessage(model.RoleUser, "run")})
	if err != nil || result.Message.Text() != "recovered" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestPendingInputContinuesAsANormalNextTurn(t *testing.T) {
	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "ask-1", Name: "ask", Arguments: json.RawMessage(`{}`)}}}}, FinishReason: model.FinishToolCalls},
		{Message: model.TextMessage(model.RoleAssistant, "Deploying to staging."), FinishReason: model.FinishStop},
	}}
	catalog, _ := tool.NewCatalog(askTool{})
	runner, _ := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	first, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "pending-followup", TurnID: "first", Input: model.TextMessage(model.RoleUser, "deploy")})
	if err != nil || first.Termination != TerminationPendingInput {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "pending-followup", TurnID: "second", Input: model.TextMessage(model.RoleUser, "staging")})
	if err != nil || second.Message.Text() != "Deploying to staging." {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	request := client.requests[1]
	var sawQuestion, sawAnswer bool
	for _, message := range request.Messages {
		sawAnswer = sawAnswer || message.Text() == "staging"
		for _, content := range message.Content {
			if content.ToolResult != nil {
				for _, resultContent := range content.ToolResult.Content {
					sawQuestion = sawQuestion || resultContent.Text == "Which environment should I use?"
				}
			}
		}
	}
	if !sawQuestion || !sawAnswer {
		t.Fatalf("follow-up context=%+v", request.Messages)
	}
}

func TestRunTurnStopsAtStepLimitWithoutSyntheticModelCall(t *testing.T) {
	call := model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "one", Name: "echo", Arguments: json.RawMessage(`{"value":"evidence"}`)}}}}
	client := &scriptedModel{responses: []model.Response{{Message: call, FinishReason: model.FinishToolCalls}}}
	catalog, _ := tool.NewCatalog(echoTool{})
	runner, _ := New(Config{Model: "test", MaxSteps: 1}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	result, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "step-limit", Input: model.TextMessage(model.RoleUser, "investigate")})
	if err == nil || result.Termination != TerminationMaxSteps {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests=%+v", client.requests)
	}
}

func TestRunTurnReturnsLengthLimitedResponseWithoutContinuationPrompt(t *testing.T) {
	client := &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "partial"), FinishReason: model.FinishLength}}}
	catalog, _ := tool.NewCatalog()
	runner, _ := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	result, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "output-limit", Input: model.TextMessage(model.RoleUser, "write")})
	if err != nil || result.Termination != TerminationOutputLimit || result.Message.Text() != "partial" || len(client.requests) != 1 {
		t.Fatalf("result=%+v requests=%d err=%v", result, len(client.requests), err)
	}
}

func TestRunTurnRunsParallelSafeToolsConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	call := func(id, name string) model.Content {
		return model.Content{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(`{}`)}}
	}
	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{call("1", "a"), call("2", "b")}}, FinishReason: model.FinishToolCalls},
		{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop},
	}}
	catalog, _ := tool.NewCatalog(parallelProbeTool{name: "a", started: started, release: release}, parallelProbeTool{name: "b", started: started, release: release})
	runner, _ := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	done := make(chan error, 1)
	go func() {
		_, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "parallel", Input: model.TextMessage(model.RoleUser, "run")})
		done <- err
	}()
	seen := map[string]bool{<-started: true, <-started: true}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("started=%v", seen)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunTurnRecordsCancellationAndReleasesSessionLock(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	store := transcript.NewMemoryStore()
	runner, _ := New(Config{Model: "test"}, Dependencies{Model: blockingModel{}, Tools: catalog, Transcript: store})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := runner.RunTurn(ctx, TurnRequest{SessionID: "cancel", Input: model.TextMessage(model.RoleUser, "wait")})
	if err == nil || result.Termination != TerminationCanceled {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	runner.lockMu.Lock()
	locks := len(runner.locks)
	runner.lockMu.Unlock()
	if locks != 0 {
		t.Fatalf("session locks retained=%d", locks)
	}
	events, _ := store.Load(context.Background(), "cancel", 0)
	if events[len(events)-1].Type != transcript.TurnCanceled {
		t.Fatalf("last event=%+v", events[len(events)-1])
	}
}

func TestRunTurnProjectsSteeringBeforeModelCall(t *testing.T) {
	client := &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop}}}
	catalog, _ := tool.NewCatalog()
	steering := &InputBuffer{}
	steering.Push(model.TextMessage(model.RoleUser, "focus on postgres"))
	runner, _ := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	if _, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "steer", Input: model.TextMessage(model.RoleUser, "diagnose"), Steering: steering}); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, message := range client.requests[0].Messages {
		found = found || message.Text() == "focus on postgres"
	}
	if !found {
		t.Fatalf("messages=%+v", client.requests[0].Messages)
	}
}

func TestRunTurnDoesNotPersistInlineImageBytes(t *testing.T) {
	client := &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop}}}
	catalog, _ := tool.NewCatalog()
	store := transcript.NewMemoryStore()
	runner, _ := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	input := model.Message{Role: model.RoleUser, Content: []model.Content{
		{Type: model.ContentText, Text: "inspect"},
		{Type: model.ContentImage, ImageURL: "data:image/png;base64," + strings.Repeat("A", 4096)},
	}}
	if _, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "image", TurnID: "image-turn", Input: input}); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 || client.requests[0].Messages[len(client.requests[0].Messages)-1].Content[1].ImageURL == "" {
		t.Fatalf("current model request lost image: %+v", client.requests)
	}
	events, _ := store.Load(context.Background(), "image", 0)
	for _, event := range events {
		if event.Message == nil {
			continue
		}
		for _, content := range event.Message.Content {
			if strings.HasPrefix(content.ImageURL, "data:") {
				t.Fatal("inline image bytes were persisted")
			}
		}
	}
}

func TestRunTurnCompactsDroppedContext(t *testing.T) {
	store := transcript.NewMemoryStore()
	for index := 0; index < 6; index++ {
		message := model.TextMessage(model.RoleUser, strings.Repeat("history ", 50))
		_, _ = store.Append(context.Background(), transcript.Event{ID: fmt.Sprintf("old-%d", index), SessionID: "compact", Type: transcript.UserInput, Message: &message})
	}
	client := &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop}}}
	catalog, _ := tool.NewCatalog()
	compactor := &recordingCompactor{}
	runner, _ := New(Config{Model: "test", Context: ContextConfig{MaxTokens: 120, ReserveTokens: 20}}, Dependencies{Model: client, Tools: catalog, Transcript: store, Compactor: compactor})
	if _, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "compact", Input: model.TextMessage(model.RoleUser, "new")}); err != nil {
		t.Fatal(err)
	}
	if !compactor.called {
		t.Fatal("compactor was not called")
	}
}

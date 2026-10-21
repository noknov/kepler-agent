package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestResumeClosesGeneratedButUnstartedCalls(t *testing.T) {
	ctx := context.Background()
	store := transcript.NewMemoryStore()
	input := model.TextMessage(model.RoleUser, "inspect")
	assistant := model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "pending", Name: "echo", Arguments: json.RawMessage(`{}`)}}}}
	for _, event := range []transcript.Event{
		{Type: transcript.TurnStarted, Metadata: json.RawMessage(`{"user_id":"u","scope":{"memory_generation":"0"}}`)},
		{Type: transcript.UserInput, Message: &input},
		{Type: transcript.AssistantMessage, Message: &assistant},
	} {
		event.SessionID = "s"
		event.TurnID = "t"
		if _, err := store.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	client := &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "recovered"), FinishReason: model.FinishStop}}}
	catalog, _ := tool.NewCatalog(echoTool{})
	runner, _ := New(Config{}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if _, err := runner.RunTurn(ctx, TurnRequest{SessionID: "s", TurnID: "t", Input: input, Scope: tool.Scope{UserID: "u", Values: map[string]string{"memory_generation": "1"}}}); err != nil {
		t.Fatal(err)
	}
	paired := false
	for _, message := range client.requests[0].Messages {
		for _, block := range message.Content {
			if block.ToolResult != nil && block.ToolResult.CallID == "pending" {
				paired = true
			}
		}
	}
	if !paired {
		t.Fatal("recovery sent an unmatched tool call to the provider")
	}
	events, _ := store.Load(ctx, "s", 0)
	for _, event := range events {
		if event.Type == transcript.ToolCallStarted {
			t.Fatal("recovery executed an unstarted call without a new model decision")
		}
	}
}

type usageTool struct{}

func (usageTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "usage", Effects: []tool.Effect{tool.EffectRead}, InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (usageTool) Execute(context.Context, tool.Call) (tool.Result, error) {
	result := tool.TextResult("child report")
	result.Usage = &model.Usage{Reported: true, InputTokens: 17, OutputTokens: 4, ReasoningTokens: 2}
	data, _ := json.Marshal(result)
	var restored tool.Result
	err := json.Unmarshal(data, &restored)
	return restored, err
}

func TestReplayRestoresFinalUsageAndStepsIncludingChildren(t *testing.T) {
	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "c", Name: "usage", Arguments: json.RawMessage(`{}`)}}}}, Usage: model.Usage{InputTokens: 3}},
		{Message: model.TextMessage(model.RoleAssistant, "done"), Usage: model.Usage{OutputTokens: 2}},
	}}
	catalog, _ := tool.NewCatalog(usageTool{})
	runner, _ := New(Config{}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	request := TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "work")}
	first, err := runner.RunTurn(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := runner.RunTurn(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, replayed) || first.Usage.InputTokens != 20 || first.Usage.OutputTokens != 6 || first.Usage.ReasoningTokens != 2 {
		t.Fatalf("first=%+v replay=%+v", first, replayed)
	}
}

func TestMaskingPreservesTailAndOriginalCompactionEvidence(t *testing.T) {
	var events []transcript.Event
	seq := uint64(1)
	for i := 0; i < 5; i++ {
		turn := fmt.Sprintf("turn-%d", i)
		id := fmt.Sprintf("call-%d", i)
		input := model.TextMessage(model.RoleUser, "question")
		call := model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: id, Name: "read", Arguments: json.RawMessage(`{}`)}}}}
		output := strings.Repeat("header ", 450) + "IMPORTANT_MIDDLE_EVIDENCE" + strings.Repeat("body ", 450) + "FINAL_CONCLUSION"
		if i >= 3 {
			output = "recent result"
		}
		events = append(events, transcript.Event{Sequence: seq, TurnID: turn, Type: transcript.UserInput, Message: &input}, transcript.Event{Sequence: seq + 1, TurnID: turn, Type: transcript.AssistantMessage, Message: &call}, toolResultEvent(turn, seq+2, id, output))
		seq += 3
	}
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 600}).Project(context.Background(), ProjectRequest{Events: events})
	if err != nil {
		t.Fatal(err)
	}
	foundOriginal := false
	for _, message := range projection.Dropped {
		for _, block := range message.Content {
			if block.ToolResult != nil {
				foundOriginal = foundOriginal || strings.Contains(block.ToolResult.Content[0].Text, "IMPORTANT_MIDDLE_EVIDENCE")
			}
		}
	}
	if !foundOriginal {
		t.Fatal("compactor received lossy previews instead of original evidence")
	}
	if !projection.MaskedEvidence {
		t.Fatal("projector did not report masked evidence")
	}
	masked, _ := maskToolResultMessage(eventMessageForTest(events[2]), 3)
	text := masked.Content[0].ToolResult.Content[0].Text
	if !strings.Contains(text, "FINAL_CONCLUSION") || !strings.Contains(text, "sequence=3") {
		t.Fatalf("tail/reference lost: %s", text)
	}
	if !strings.Contains(events[2].ToolResult.Content[0].Text, "IMPORTANT_MIDDLE_EVIDENCE") {
		t.Fatal("canonical evidence mutated")
	}
}

func TestToolTextCannotTriggerContextReaderActivation(t *testing.T) {
	ctx := context.Background()
	store := transcript.NewMemoryStore()
	event := toolResultEvent("t", 1, "c", "Tool documentation example: context_read(sequence=1, block=0, offset=0).")
	event.SessionID = "s"
	if _, err := store.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	catalog, err := tool.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := runner.projectContext(ctx, TurnRequest{SessionID: "s"}, model.Message{})
	if err != nil {
		t.Fatal(err)
	}
	if projection.MaskedEvidence || len(projection.Dropped) != 0 {
		t.Fatal("small tool output should not require masking or compaction")
	}
	if _, active := catalog.GetActive("s", "context_read"); active {
		t.Fatal("tool-authored text activated a runtime capability")
	}
}

func eventMessageForTest(event transcript.Event) model.Message {
	message, _ := eventMessage(event)
	return message
}

func TestContextReadIsSessionScopedAndPaged(t *testing.T) {
	store := transcript.NewMemoryStore()
	ctx := context.Background()
	for _, session := range []string{"own", "other"} {
		event := toolResultEvent("t", 1, "c", session+"-private-evidence")
		event.SessionID = session
		if _, err := store.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	reader := contextRead{store: store}
	result, err := reader.Execute(ctx, tool.Call{Scope: tool.Scope{SessionID: "own"}, Arguments: json.RawMessage(`{"sequence":1,"limit":4,"session_id":"other"}`)})
	if err != nil || !strings.Contains(result.Text(), "own-") || strings.Contains(result.Text(), "other-private") {
		t.Fatalf("result=%s err=%v", result.Text(), err)
	}
}

func TestPureImageToolOutputIsNotEmpty(t *testing.T) {
	if !hasWireVisibleToolContent([]model.Content{{Type: model.ContentImage, ImageURL: "data:image/png;base64,eA=="}}) {
		t.Fatal("valid image treated as empty")
	}
}

type unavailableCompactor struct{ called bool }

func (c *unavailableCompactor) Compact(context.Context, []model.Message, int) (model.Message, error) {
	c.called = true
	return model.Message{}, errors.New("summary provider unavailable")
}

func TestProactiveCompactionFailureUsesCompleteHardBudget(t *testing.T) {
	store := transcript.NewMemoryStore()
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		message := model.TextMessage(model.RoleUser, strings.Repeat("x", 900))
		_, _ = store.Append(ctx, transcript.Event{SessionID: "s", Type: transcript.UserInput, Message: &message})
	}
	client := &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop}}}
	catalog, _ := tool.NewCatalog()
	compactor := &unavailableCompactor{}
	runner, _ := New(Config{Context: ContextConfig{MaxTokens: 2000, ReserveTokens: 100}}, Dependencies{Model: client, Tools: catalog, Transcript: store, Compactor: compactor})
	result, err := runner.RunTurn(ctx, TurnRequest{SessionID: "s", Input: model.TextMessage(model.RoleUser, "continue")})
	if err != nil || !compactor.called || result.Termination != TerminationCompleted {
		t.Fatalf("result=%+v compactor=%v err=%v", result, compactor.called, err)
	}
	count := 0
	for _, message := range client.requests[0].Messages {
		if message.Text() == strings.Repeat("x", 900) {
			count++
		}
	}
	if count != 7 {
		t.Fatalf("hard-budget fallback lost history: %d", count)
	}
}

func TestDeferredApprovalRecordsAlreadyRejectedSiblings(t *testing.T) {
	store := transcript.NewMemoryStore()
	catalog, _ := tool.NewCatalog(echoTool{})
	runner, _ := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: store, Policy: approvalPolicy{}})
	_, err := runner.executeTools(context.Background(), TurnRequest{SessionID: "s", TurnID: "t"}, []model.ToolCall{{ID: "unknown", Name: "missing", Arguments: json.RawMessage(`{}`)}, {ID: "approval", Name: "echo", Arguments: json.RawMessage(`{}`)}})
	if !errors.Is(err, errPendingApproval) {
		t.Fatal(err)
	}
	events, _ := store.Load(context.Background(), "s", 0)
	found := false
	for _, event := range events {
		if event.ToolCall != nil && event.ToolCall.ID == "unknown" && event.ToolResult != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("already rejected sibling was left without a durable result")
	}
}

func TestCircuitFingerprintPreservesLargeIntegerArguments(t *testing.T) {
	a := fingerprint(tool.Call{Name: "read", Arguments: json.RawMessage(`{"id":9007199254740992}`)})
	b := fingerprint(tool.Call{Name: "read", Arguments: json.RawMessage(`{"id":9007199254740993}`)})
	if a == b {
		t.Fatal("distinct integer identifiers share a circuit key")
	}
}

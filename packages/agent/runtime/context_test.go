package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestApprovalProjectionPreservesOpaqueCallOwner(t *testing.T) {
	pending := tool.Call{ID: "c", Name: "write"}
	completed := tool.Call{ID: "b\x00c", Name: "write"}
	result := tool.TextResult("another call completed")
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 2000}).Project(t.Context(), ProjectRequest{Events: []transcript.Event{
		{Sequence: 1, TurnID: "a\x00b", Type: transcript.ApprovalRequested, ToolCall: &pending},
		{Sequence: 2, TurnID: "a", Type: transcript.ToolCallCompleted, ToolCall: &completed, ToolResult: &result},
	}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range projection.Messages {
		for _, block := range message.Content {
			if block.ToolResult != nil && block.ToolResult.CallID == pending.ID {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("another turn's completed call hid a pending approval")
	}
}

func TestBoundedProjectorDropsOldMessagesAndPreservesRecentTurn(t *testing.T) {
	events := []transcript.Event{
		{Sequence: 1, Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, strings.Repeat("old ", 100)))},
		{Sequence: 2, Type: transcript.AssistantMessage, Message: messagePtr(model.TextMessage(model.RoleAssistant, strings.Repeat("answer ", 100)))},
		{Sequence: 3, Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, "recent question"))},
		{Sequence: 4, Type: transcript.AssistantMessage, Message: messagePtr(model.TextMessage(model.RoleAssistant, "recent answer"))},
	}
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 80, ReserveTokens: 10}).Project(context.Background(), ProjectRequest{Events: events, System: model.TextMessage(model.RoleSystem, "system")})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Dropped) == 0 || projection.CoversThrough == 0 {
		t.Fatalf("projection=%+v", projection)
	}
	if got := projection.Messages[len(projection.Messages)-1].Text(); got != "recent answer" {
		t.Fatalf("last message=%q", got)
	}
}

func TestProjectionUsesLatestCompactionCoverage(t *testing.T) {
	summary := model.TextMessage(model.RoleUser, "<untrusted_transcript_summary>\nsummary\n</untrusted_transcript_summary>")
	old := model.TextMessage(model.RoleUser, "old")
	newer := model.TextMessage(model.RoleUser, "new")
	events := []transcript.Event{
		{Sequence: 1, Type: transcript.UserInput, Message: &old},
		{Sequence: 2, Type: transcript.CompactionCreated, Message: &summary, Metadata: []byte(`{"covers_through":1}`)},
		{Sequence: 3, Type: transcript.UserInput, Message: &newer},
	}
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 1000}).Project(context.Background(), ProjectRequest{Events: events, System: model.Message{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Messages) != 3 || projection.Messages[0].Role != model.RoleSystem || projection.Messages[0].Text() != untrustedTranscriptSummaryBoundary.Text() || projection.Messages[1].Text() != summary.Text() || projection.Messages[2].Text() != "new" {
		t.Fatalf("messages=%+v", projection.Messages)
	}
}

func TestProjectionIgnoresCompactionWithInvalidCoverage(t *testing.T) {
	old := model.TextMessage(model.RoleUser, "old")
	bad := model.TextMessage(model.RoleUser, "bad summary")
	newer := model.TextMessage(model.RoleUser, "new")
	events := []transcript.Event{
		{Sequence: 1, Type: transcript.UserInput, Message: &old},
		{Sequence: 2, Type: transcript.CompactionCreated, Message: &bad},
		{Sequence: 3, Type: transcript.UserInput, Message: &newer},
	}
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 1000}).Project(context.Background(), ProjectRequest{Events: events, System: model.Message{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Messages) != 2 || projection.Messages[0].Text() != "old" || projection.Messages[1].Text() != "new" {
		t.Fatalf("invalid compaction changed history: %+v", projection.Messages)
	}
}

func TestBoundedProjectorNeverSplitsToolCallAndResult(t *testing.T) {
	call := model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call-1", Name: "read", Arguments: []byte(`{}`)}}}}
	result := toolResultEvent("turn-old", 3, "call-1", strings.Repeat("result ", 100))
	events := []transcript.Event{
		{Sequence: 1, TurnID: "turn-old", Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, strings.Repeat("old ", 100)))},
		{Sequence: 2, TurnID: "turn-old", Type: transcript.AssistantMessage, Message: &call},
		result,
		{Sequence: 4, TurnID: "turn-new", Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, "new question"))},
		{Sequence: 5, TurnID: "turn-new", Type: transcript.AssistantMessage, Message: messagePtr(model.TextMessage(model.RoleAssistant, "new answer"))},
		{Sequence: 6, TurnID: "turn-last", Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, "last question"))},
	}
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 80}).Project(context.Background(), ProjectRequest{Events: events, System: model.Message{}})
	if err != nil {
		t.Fatal(err)
	}
	var sawCall, sawResult bool
	for _, message := range projection.Messages {
		sawCall = sawCall || len(message.ToolCalls()) > 0
		for _, content := range message.Content {
			sawResult = sawResult || content.ToolResult != nil
		}
	}
	if sawCall != sawResult {
		t.Fatalf("tool call/result pair was split: messages=%+v", projection.Messages)
	}
}

func TestEstimateTokensCountsCJKAndBoundsImagesByConstant(t *testing.T) {
	text := EstimateTokens([]model.Message{model.TextMessage(model.RoleUser, strings.Repeat("中", 100))})
	if text < 100 {
		t.Fatalf("unexpected CJK estimate: %d", text)
	}
	small := EstimateTokens([]model.Message{{Role: model.RoleUser, Content: []model.Content{{Type: model.ContentImage, ImageURL: "data:image/png;base64," + strings.Repeat("A", 400)}}}})
	large := EstimateTokens([]model.Message{{Role: model.RoleUser, Content: []model.Content{{Type: model.ContentImage, ImageURL: "data:image/png;base64," + strings.Repeat("A", 400000)}}}})
	// Image cost must be a per-image constant, independent of payload length;
	// otherwise one screenshot would consume the whole context budget.
	if small != large {
		t.Fatalf("image estimate depends on payload length: small=%d large=%d", small, large)
	}
	if small < 1024 || small > 1200 {
		t.Fatalf("image estimate = %d, want a bounded per-image constant", small)
	}
}

func TestBuildBudgetAccountsForToolsAndOutput(t *testing.T) {
	// Roughly 800 estimated tokens, an indivisible latest turn.
	events := []transcript.Event{
		{Sequence: 1, Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, strings.Repeat("x ", 1600)))},
	}
	tools := []model.ToolDefinition{{
		Name:        "read_file",
		Description: strings.Repeat("d ", 800),
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}}
	// With only the autocompact reserve the turn fits.
	if _, err := NewBoundedProjector(ContextConfig{MaxTokens: 2000, ReserveTokens: 100}).Project(context.Background(), ProjectRequest{Events: events}); err != nil {
		t.Fatalf("projection without tool or output reserve should fit: %v", err)
	}
	// Reserving the response cap and the tool schemas must shrink the budget
	// below the same turn, so it can no longer silently pass.
	if _, err := NewBoundedProjector(ContextConfig{MaxTokens: 2000, ReserveTokens: 100, OutputTokens: 1000}).Project(context.Background(), ProjectRequest{Events: events, Tools: tools}); err == nil {
		t.Fatal("expected tool and output reserves to reduce the budget and reject the turn")
	}
	if got := EstimateToolTokens(tools); got <= 0 {
		t.Fatalf("EstimateToolTokens() = %d, want positive", got)
	}
	if got := EstimateToolTokens(nil); got != 0 {
		t.Fatalf("EstimateToolTokens(nil) = %d, want 0", got)
	}
}

func TestBoundedProjectorRejectsOversizedLatestTurn(t *testing.T) {
	message := model.TextMessage(model.RoleUser, strings.Repeat("large ", 500))
	_, err := NewBoundedProjector(ContextConfig{MaxTokens: 100}).Project(context.Background(), ProjectRequest{Events: []transcript.Event{{Sequence: 1, TurnID: "current", Type: transcript.UserInput, Message: &message}}, System: model.Message{}})
	if err == nil {
		t.Fatal("expected an oversized indivisible turn to fail")
	}
}

func toolResultEvent(turnID string, sequence uint64, callID, text string) transcript.Event {
	call := tool.Call{ID: callID, Name: "read"}
	result := tool.Result{Content: []model.Content{{Type: model.ContentText, Text: text}}}
	return transcript.Event{Sequence: sequence, TurnID: turnID, Type: transcript.ToolCallCompleted, ToolCall: &call, ToolResult: &result}
}

func messagePtr(message model.Message) *model.Message { return &message }

func TestLongCurrentTurnCompactsCompletedExchanges(t *testing.T) {
	input := model.TextMessage(model.RoleUser, "original task")
	events := []transcript.Event{{Sequence: 1, TurnID: "current", Type: transcript.UserInput, Message: &input}}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("call-%d", i)
		call := model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: id, Name: "read", Arguments: []byte(`{}`)}}}}
		events = append(events, transcript.Event{Sequence: uint64(2 + i*2), TurnID: "current", Type: transcript.AssistantMessage, Message: &call}, toolResultEvent("current", uint64(3+i*2), id, strings.Repeat("data ", 60)))
	}
	p := NewBoundedProjector(ContextConfig{MaxTokens: 230})
	projection, err := p.Project(context.Background(), ProjectRequest{Events: events})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Dropped) == 0 || projection.CoversThrough == 0 {
		t.Fatal("no completed exchanges compacted")
	}
	check := func(messages []model.Message) {
		t.Helper()
		pending := map[string]bool{}
		found := false
		for _, m := range messages {
			found = found || m.Text() == "original task"
			for _, c := range m.ToolCalls() {
				pending[c.ID] = true
			}
			for _, b := range m.Content {
				if b.ToolResult != nil {
					if !pending[b.ToolResult.CallID] {
						t.Fatal("orphan tool result")
					}
					delete(pending, b.ToolResult.CallID)
				}
			}
		}
		if !found || len(pending) != 0 {
			t.Fatalf("input or tool pairing lost: %v", pending)
		}
	}
	check(projection.Messages)
	summary := model.TextMessage(model.RoleUser, "summary")
	events = append(events, transcript.Event{Sequence: 14, Type: transcript.CompactionCreated, Message: &summary, Metadata: []byte(fmt.Sprintf(`{"covers_through":%d}`, projection.CoversThrough))})
	projection, err = p.Project(context.Background(), ProjectRequest{Events: events})
	if err != nil {
		t.Fatal(err)
	}
	check(projection.Messages)
}

func TestBoundedProjectorObservationMaskingAvoidsDroppingTurns(t *testing.T) {
	input1 := model.TextMessage(model.RoleUser, "first question")
	call1 := model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call-1", Name: "read_file", Arguments: []byte(`{"path":"large.go"}`)}}}}
	largeOutput := strings.Repeat("package main // line of code\n", 200) // ~5800 bytes
	events := []transcript.Event{
		{Sequence: 1, TurnID: "turn-1", Type: transcript.UserInput, Message: &input1},
		{Sequence: 2, TurnID: "turn-1", Type: transcript.AssistantMessage, Message: &call1},
		toolResultEvent("turn-1", 3, "call-1", largeOutput),
		{Sequence: 4, TurnID: "turn-1", Type: transcript.AssistantMessage, Message: messagePtr(model.TextMessage(model.RoleAssistant, "done 1"))},
		{Sequence: 5, TurnID: "turn-2", Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, "second question"))},
		{Sequence: 6, TurnID: "turn-2", Type: transcript.AssistantMessage, Message: messagePtr(model.TextMessage(model.RoleAssistant, "done 2"))},
		{Sequence: 7, TurnID: "turn-3", Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, "third question"))},
	}
	// Budget fits all 3 turns once the 5800-byte historical tool output in turn-1 is masked,
	// without needing to drop turn-1 or trigger LLM compaction.
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 600}).Project(context.Background(), ProjectRequest{Events: events})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Dropped) != 0 {
		t.Fatalf("expected observation masking to satisfy budget without dropping turns, dropped=%d", len(projection.Dropped))
	}
	foundMasked := false
	for _, msg := range projection.Messages {
		for _, block := range msg.Content {
			if block.ToolResult != nil && len(block.ToolResult.Content) > 0 && strings.Contains(block.ToolResult.Content[0].Text, "masked:") {
				foundMasked = true
			}
		}
	}
	if !foundMasked {
		t.Fatal("expected historical tool output to be masked")
	}
}

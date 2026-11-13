package runtime

import (
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestForcedProjectionPreservesCurrentInputAndCompleteParallelBatch(t *testing.T) {
	input := model.TextMessage(model.RoleUser, "Current constraints must remain explicit.")
	old := model.TextMessage(model.RoleAssistant, "Older completed investigation.")
	calls := model.Message{Role: model.RoleAssistant, Content: []model.Content{
		{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "a", Name: "read", Arguments: []byte(`{}`)}},
		{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "b", Name: "read", Arguments: []byte(`{}`)}},
	}}
	request := ProjectRequest{CompactHistory: true, Events: []transcript.Event{
		{Sequence: 1, TurnID: "turn", Type: transcript.UserInput, Message: &input},
		{Sequence: 2, TurnID: "turn", Type: transcript.AssistantMessage, Message: &old},
		{Sequence: 3, TurnID: "turn", Type: transcript.AssistantMessage, Message: &calls},
		toolResultEvent("turn", 4, "a", "evidence a"),
		toolResultEvent("turn", 5, "b", "evidence b"),
	}}
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 12000, ReserveTokens: 1000}).Project(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Dropped) != 2 || projection.Dropped[0].Text() != input.Text() || projection.Dropped[1].Text() != old.Text() {
		t.Fatalf("lost sampled input evidence or summarized a partial tool batch: %+v", projection.Dropped)
	}
	if len(projection.Messages) != 4 || projection.Messages[0].Text() != input.Text() || len(projection.Messages[1].ToolCalls()) != 2 {
		t.Fatalf("lost current input or batch: %+v", projection.Messages)
	}
	if projection.CoversThrough != 2 {
		t.Fatalf("coverage crosses the live batch: %d", projection.CoversThrough)
	}
}

func TestForcedProjectionDoesNotCreateCompactionFromOnlyCurrentInput(t *testing.T) {
	input := model.TextMessage(model.RoleUser, "Do not compress an input that has never been sampled.")
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 12000}).Project(t.Context(), ProjectRequest{
		CompactHistory: true,
		Events:         []transcript.Event{{Sequence: 1, TurnID: "turn", Type: transcript.UserInput, Message: &input}},
	})
	if err != nil || len(projection.Dropped) != 0 || len(projection.Messages) != 1 || projection.Messages[0].Text() != input.Text() {
		t.Fatalf("unexpected projection: %+v error=%v", projection, err)
	}
}

func TestCompactionCannotHideAnUnseenSteeringBatch(t *testing.T) {
	messages := []model.Message{
		model.TextMessage(model.RoleUser, "Original sampled request"),
		model.TextMessage(model.RoleAssistant, "Earlier completed work"),
		model.TextMessage(model.RoleUser, "First correction: preserve the API"),
		model.TextMessage(model.RoleUser, "Second correction: only inspect production logs"),
		model.TextMessage(model.RoleAssistant, "Recent completed exchange"),
	}
	var events []transcript.Event
	for i := range messages {
		kind := transcript.SteeringInput
		if messages[i].Role == model.RoleAssistant {
			kind = transcript.AssistantMessage
		} else if i == 0 {
			kind = transcript.UserInput
		}
		events = append(events, transcript.Event{Sequence: uint64(i + 1), TurnID: "turn", Type: kind, Message: &messages[i]})
	}
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 12000}).Project(t.Context(), ProjectRequest{
		TurnID: "turn", SampledThrough: 2, CompactHistory: true, Events: events,
	})
	if err != nil {
		t.Fatal(err)
	}
	if projection.CoversThrough != 2 || len(projection.Dropped) != 2 || len(projection.Messages) != 3 {
		t.Fatalf("pending input crossed checkpoint coverage: %+v", projection)
	}
	for i, message := range projection.Messages {
		if message.Text() != messages[i+2].Text() {
			t.Fatal("a correction was summarized or reordered before inference")
		}
	}
}

func TestUnseenInputsSurviveALegacyCheckpointCoverage(t *testing.T) {
	old := model.TextMessage(model.RoleUser, "already sampled")
	first := model.TextMessage(model.RoleUser, "first unseen correction")
	second := model.TextMessage(model.RoleUser, "second unseen correction")
	checkpoint := model.TextMessage(model.RoleUser, "older checkpoint")
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 12000}).Project(t.Context(), ProjectRequest{
		TurnID: "turn", SampledThrough: 1, CompactHistory: true,
		Events: []transcript.Event{
			{Sequence: 1, TurnID: "turn", Type: transcript.UserInput, Message: &old},
			{Sequence: 2, TurnID: "turn", Type: transcript.SteeringInput, Message: &first},
			{Sequence: 3, TurnID: "turn", Type: transcript.SteeringInput, Message: &second},
			{Sequence: 4, TurnID: "turn", Type: transcript.CompactionCreated, Message: &checkpoint, Metadata: []byte(`{"covers_through":3}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if projection.CoversThrough != 1 {
		t.Fatalf("checkpoint hides pending input on restart: coverage=%d", projection.CoversThrough)
	}
	if len(projection.Messages) < 2 || projection.Messages[len(projection.Messages)-2].Text() != first.Text() || projection.Messages[len(projection.Messages)-1].Text() != second.Text() {
		t.Fatalf("legacy coverage hid a pending correction: %+v", projection.Messages)
	}
}

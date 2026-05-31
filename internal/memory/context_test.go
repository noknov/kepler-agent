package memory

import "testing"

func TestFilterPersistentTurnsRemovesToolErrors(t *testing.T) {
	turns := []Turn{
		{Role: RoleUser, Content: "deploy this"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "github-dispatch_workflow:0", Name: "github-dispatch_workflow", Arguments: `{"workflow":"all-services"}`}}},
		{Role: RoleTool, Content: "[tool error] repository is required unless GITHUB_DEFAULT_OWNER and GITHUB_DEFAULT_REPO are configured"},
		{Role: RoleAssistant, Content: "done"},
	}

	got := FilterPersistentTurns(turns)
	if len(got) != 2 {
		t.Fatalf("len(FilterPersistentTurns()) = %d, want 2", len(got))
	}
	for _, turn := range got {
		if turn.Role == RoleTool || len(turn.ToolCalls) > 0 {
			t.Fatalf("unexpected transient tool history persisted: %#v", turn)
		}
	}
}

func TestBuildWithPartsSkipsPersistedToolErrors(t *testing.T) {
	builder := Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000}
	messages := builder.BuildWithParts(
		"system",
		"",
		"retry deploy",
		nil,
		"",
		[]Turn{
			{Role: RoleUser, Content: "deploy"},
			{Role: RoleTool, Content: "[tool error] repository is required unless GITHUB_DEFAULT_OWNER and GITHUB_DEFAULT_REPO are configured"},
			{Role: RoleAssistant, Content: "old reply"},
		},
	)

	for _, msg := range messages {
		if msg.Role == "tool" {
			t.Fatalf("tool error should not be replayed into model messages: %#v", msg)
		}
	}
}

func TestFilterPersistentTurnsKeepsMatchedToolCalls(t *testing.T) {
	turns := []Turn{
		{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{
				{ID: "tool_1", Name: "github-dispatch_workflow", Arguments: `{"workflow":"all-services"}`},
				{ID: "tool_2", Name: "code-search", Arguments: `{"query":"foo"}`},
			},
		},
		{Role: RoleTool, ToolCallID: "tool_1", Content: "dispatched"},
		{Role: RoleTool, ToolCallID: "tool_2", Content: "[tool error] temporary"},
	}

	got := FilterPersistentTurns(turns)
	if len(got) != 2 {
		t.Fatalf("len(FilterPersistentTurns()) = %d, want 2", len(got))
	}
	if len(got[0].ToolCalls) != 1 || got[0].ToolCalls[0].ID != "tool_1" {
		t.Fatalf("assistant tool calls were not filtered correctly: %#v", got[0].ToolCalls)
	}
	if got[1].ToolCallID != "tool_1" {
		t.Fatalf("unexpected tool turn kept: %#v", got[1])
	}
}

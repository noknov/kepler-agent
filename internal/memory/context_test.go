package memory

import "testing"

func TestFilterPersistentTurnsRemovesToolErrors(t *testing.T) {
	turns := []Turn{
		{Role: RoleUser, Content: "deploy this"},
		{Role: RoleTool, Content: "[tool error] repository is required unless GITHUB_DEFAULT_OWNER and GITHUB_DEFAULT_REPO are configured"},
		{Role: RoleAssistant, Content: "done"},
	}

	got := FilterPersistentTurns(turns)
	if len(got) != 2 {
		t.Fatalf("len(FilterPersistentTurns()) = %d, want 2", len(got))
	}
	for _, turn := range got {
		if turn.Role == RoleTool {
			t.Fatalf("unexpected tool error turn persisted: %#v", turn)
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

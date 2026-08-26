package runtime

import (
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/environment"
	"github.com/noknov/slack-copilot-agent/packages/agent/model"
)

func TestInsertEnvironmentFragmentAfterSystemMessages(t *testing.T) {
	messages := []model.Message{
		model.TextMessage(model.RoleSystem, "static rules"),
		model.TextMessage(model.RoleSystem, "compacted summary"),
		model.TextMessage(model.RoleUser, "question"),
	}
	got := insertEnvironmentFragment(messages, environment.Config{WorkspaceRoots: []string{"/workspace"}}.Message())
	if len(got) != 4 {
		t.Fatalf("messages=%+v", got)
	}
	if got[0].Text() != "static rules" || got[1].Text() != "compacted summary" {
		t.Fatalf("system messages moved: %+v", got)
	}
	if got[2].Role != model.RoleUser || got[2].ID != environment.MessageID {
		t.Fatalf("environment fragment missing at index 2: %+v", got[2])
	}
	if got[3].Text() != "question" {
		t.Fatalf("conversation order changed: %+v", got)
	}
}

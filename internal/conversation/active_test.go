package conversation

import (
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/internal/agent"
)

func TestSteeringAppliedMessageFormatting(t *testing.T) {
	got := steeringAppliedMessage(agent.LocaleZH)
	want := "\n\n_已引导对话_\n"
	if got != want {
		t.Fatalf("steeringAppliedMessage(zh) = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, "\n\n") {
		t.Fatalf("steeringAppliedMessage(zh) should start with blank line for italic: %q", got)
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Fatalf("steeringAppliedMessage(zh) should not end with double newline: %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("steeringAppliedMessage(zh) should end with single newline: %q", got)
	}
}

func TestSteeringCardAndBodyTextDiffer(t *testing.T) {
	card := agent.SteeringQueuedTitle(agent.LocaleZH)
	body := steeringAppliedMessage(agent.LocaleZH)
	if card != "对话引导中..." {
		t.Fatalf("SteeringQueuedTitle(zh) = %q, want 对话引导中...", card)
	}
	if body != "\n\n_已引导对话_\n" {
		t.Fatalf("steeringAppliedMessage(zh) = %q, want \\n\\n_已引导对话_\\n", body)
	}
	if card == body {
		t.Fatal("steering card title and stream body text should differ")
	}
}

package cli

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
)

func TestRenderZoneExactHeight(t *testing.T) {
	got := renderZone("a\nb\nc\nd\ne", 3)
	if lineCount(got) != 3 {
		t.Fatalf("zone lines = %d, want 3: %q", lineCount(got), got)
	}
}

func TestRenderZonePadsToHeight(t *testing.T) {
	got := renderZone("a\nb\nc", 6)
	if lineCount(got) != 6 {
		t.Fatalf("padded zone lines = %d, want 6: %q", lineCount(got), got)
	}
}

func TestRenderZoneStickyBottom(t *testing.T) {
	content := "1\n2\n3\n4\n5"
	got := renderZone(content, 3)
	if got != "3\n4\n5" {
		t.Fatalf("sticky bottom = %q", got)
	}
}

func TestJoinLinesInputZone(t *testing.T) {
	got := joinLines([]string{"top", "mid", "bot"}, inputZoneLines)
	if lineCount(got) != inputZoneLines {
		t.Fatalf("input zone lines = %d", lineCount(got))
	}
}

func TestViewFrameHeight(t *testing.T) {
	m := &sessionUI{
		width:  80,
		height: 24,
		styles: newTUIStyles(false),
		input:  textinput.New(),
	}
	frame := m.View()
	if lineCount(frame) != 24 {
		t.Fatalf("frame lines = %d, want 24", lineCount(frame))
	}
}

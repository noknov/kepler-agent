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

func TestFrameLayoutRenderHeight(t *testing.T) {
	layout := FrameLayout{TermHeight: 24, BottomLines: defaultBottomLines}
	bottom := joinLines([]string{"a", "b", "c"}, defaultBottomLines)
	frame := layout.Render("header\nline", bottom)
	if lineCount(frame) != 24 {
		t.Fatalf("frame lines = %d, want 24", lineCount(frame))
	}
}

func TestJoinLinesBottomZone(t *testing.T) {
	got := joinLines([]string{"top", "mid", "bot"}, defaultBottomLines)
	if lineCount(got) != defaultBottomLines {
		t.Fatalf("bottom zone lines = %d", lineCount(got))
	}
}

func TestREPLViewFrameHeight(t *testing.T) {
	m := &replModel{
		width:  80,
		height: 24,
		styles: newTUIStyles(false),
		input:  textinput.New(),
		layout: FrameLayout{TermHeight: 24, BottomLines: defaultBottomLines},
	}
	frame := m.View()
	if lineCount(frame) != 24 {
		t.Fatalf("frame lines = %d, want 24", lineCount(frame))
	}
}

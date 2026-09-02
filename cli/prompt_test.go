package cli

import "testing"

func TestFormatPromptFooter(t *testing.T) {
	foot := formatPromptFooter(40, "~/proj", "gpt-test")
	if !contains(foot, "/help") {
		t.Fatalf("missing /help: %q", foot)
	}
	if displayWidth(foot) > 40 {
		t.Fatalf("footer wider than cols: %d", displayWidth(foot))
	}
}

func TestFitLinesPadsAndClips(t *testing.T) {
	if got := lineCount("a\nb\nc"); got != 3 {
		t.Fatalf("lineCount = %d", got)
	}
	if got := fitLines("a\nb", 4); lineCount(got) != 4 {
		t.Fatalf("fitLines pad = %q", got)
	}
	if got := fitLines("a\nb\nc\nd", 2); got != "a\nb" {
		t.Fatalf("fitLines clip = %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

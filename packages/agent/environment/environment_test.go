package environment

import (
	"strings"
	"testing"
	"time"
)

func TestMessageRendersEnvironmentContext(t *testing.T) {
	now := time.Date(2026, 6, 30, 9, 15, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	message := (Config{
		WorkspaceRoots: []string{"/data/workspace"},
		Now:            func() time.Time { return now },
	}).Message()
	text := message.Text()
	for _, want := range []string{
		"<environment_context>",
		"<current_date>2026-06-30</current_date>",
		"<current_year>2026</current_year>",
		"<timezone>Asia/Shanghai</timezone>",
		"<root>/data/workspace</root>",
		"今年",
		"include 2026 in the search query",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("environment message missing %q:\n%s", want, text)
		}
	}
	if message.Role != "user" || message.ID != MessageID {
		t.Fatalf("unexpected environment message: %+v", message)
	}
}

func TestMessageOmitsWorkspaceRootsWhenUnset(t *testing.T) {
	text := (Config{Now: func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }}).Message().Text()
	if strings.Contains(text, "<workspace_roots>") {
		t.Fatalf("expected no workspace roots block:\n%s", text)
	}
}

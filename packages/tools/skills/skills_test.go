package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
)

func TestDiscoverPromptAndLoad(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "review")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: code-review\ndescription: Review risky changes.\n---\n\nFull workflow."
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := Discover([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(catalog.Prompt(), "code-review: Review risky changes.") || strings.Contains(catalog.Prompt(), "Full workflow") {
		t.Fatalf("prompt=%q", catalog.Prompt())
	}
	result, err := catalog.Tool().Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"name":"code-review"}`)})
	if err != nil || !strings.Contains(result.Text(), "Full workflow") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestDiscoverRejectsMalformedSkillMetadata(t *testing.T) {
	for _, body := range []string{"---\nname: [broken\n---\nbody", "---\nname: unterminated\n"} {
		root := t.TempDir()
		directory := filepath.Join(root, "demo")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "SKILL.md")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Discover([]string{root}); err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("invalid skill accepted or not identified: %v", err)
		}
	}
}

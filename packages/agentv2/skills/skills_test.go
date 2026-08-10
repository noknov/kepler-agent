package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
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
	result, err := catalog.Tool().Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{"name":"code-review"}`)})
	if err != nil || !strings.Contains(result.Content[0].Text, "Full workflow") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

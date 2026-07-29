package localexec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

func TestCommandToolRunsInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := CommandTool{WorkspaceRoots: []string{root}, Guard: safety.NewCommandPolicy(), Timeout: time.Second}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"argv":["cat","marker.txt"]}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.TrimSpace(result.Content) != "hello" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestCommandToolRejectsOutsideWorkdir(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	tool := CommandTool{WorkspaceRoots: []string{root}, Guard: safety.NewCommandPolicy(), Timeout: time.Second}

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"argv":["pwd"],"workdir":"`+filepath.ToSlash(other)+`"}`), registry.Runtime{})
	if err == nil {
		t.Fatal("expected outside workdir to be rejected")
	}
}

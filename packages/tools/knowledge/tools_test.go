package knowledge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
)

func TestRunbookSearchFindsLocalMarkdown(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "checkout.md"), []byte("# Checkout\nowner: payments\nalert: high checkout error rate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (RunbookSearchTool{Dir: dir}).Execute(context.Background(), json.RawMessage(`{"query":"checkout error"}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "checkout.md") || !strings.Contains(result.Content, "payments") {
		t.Fatalf("unexpected result: %q", result.Content)
	}
}

func TestRunbookSearchMissingDirReturnsNoMatches(t *testing.T) {
	result, err := (RunbookSearchTool{Dir: filepath.Join(t.TempDir(), "missing")}).Execute(context.Background(), json.RawMessage(`{"query":"anything"}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "no matching runbooks" {
		t.Fatalf("Content = %q", result.Content)
	}
}

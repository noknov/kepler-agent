package knowledge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
)

func TestRunbookSearchFindsLocalMarkdown(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "checkout.md"), []byte("# Checkout\nowner: payments\nalert: high checkout error rate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (RunbookSearchTool{Dir: dir}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"checkout error"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "checkout.md") || !strings.Contains(result.Text(), "payments") {
		t.Fatalf("unexpected result: %q", result.Text())
	}
}

func TestRunbookSearchMissingDirReturnsNoMatches(t *testing.T) {
	result, err := (RunbookSearchTool{Dir: filepath.Join(t.TempDir(), "missing")}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"anything"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text() != "no matching runbooks" {
		t.Fatalf("Content = %q", result.Text())
	}
}

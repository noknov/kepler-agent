package code

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

func TestReadFileReturnsContentDirectly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	raw := json.RawMessage(`{"path":"app.go"}`)

	result, err := tool.Execute(context.Background(), raw, registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.WaitForUser || result.Content == "" {
		t.Fatalf("expected file content, got %#v", result)
	}
}

func TestReadFileRejectsSensitiveFilesWithoutPrompting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":".env"}`), registry.Runtime{})
	if err == nil {
		t.Fatal("expected sensitive file read to be rejected")
	}
}

func TestSearchReturnsResultsDirectly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\nvar token = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"token"}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.WaitForUser || result.Content == "" || result.Content == "no matches" {
		t.Fatalf("expected search results, got %#v", result)
	}
}

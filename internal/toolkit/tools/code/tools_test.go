package code

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestReadFileAcceptsWorkspaceRootBasenamePrefix(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "wati-frontend-app")
	filePath := filepath.Join(root, "domains", "connectors", "src", "Integration", "_constants", "IntegrationCards.tsx")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("export const cards = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	raw := json.RawMessage(`{"path":"wati-frontend-app/domains/connectors/src/Integration/_constants/IntegrationCards.tsx"}`)

	result, err := tool.Execute(context.Background(), raw, registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "export const cards") {
		t.Fatalf("expected prefixed path to resolve, got %#v", result)
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

func TestSearchAcceptsWorkspaceRootBasenamePrefix(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "wati-frontend-app")
	dir := filepath.Join(root, "domains", "connectors")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.ts"), []byte("const catalog = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"catalog","path":"wati-frontend-app/domains"}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "connectors/catalog.ts") {
		t.Fatalf("expected search to resolve prefixed path, got %#v", result)
	}
}

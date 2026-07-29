package edit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

func TestWriteFileCreatesWorkspaceFile(t *testing.T) {
	root := t.TempDir()
	tool := WriteFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"src/app.go","content":"package app\n"}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "wrote src/app.go") {
		t.Fatalf("content = %q", result.Content)
	}
	got, err := os.ReadFile(filepath.Join(root, "src", "app.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "package app\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestWriteFileAcceptsWorkspaceRootBasenamePrefix(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := WriteFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"repo/main.go","content":"package main\n"}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "main.go")); err != nil {
		t.Fatal(err)
	}
}

func TestWriteFileRejectsTraversalOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	tool := WriteFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"../escape.go","content":"package escape\n"}`), registry.Runtime{})
	if err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func TestWriteFileRejectsSensitiveFile(t *testing.T) {
	root := t.TempDir()
	tool := WriteFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":".env","content":"TOKEN=x\n"}`), registry.Runtime{})
	if err == nil {
		t.Fatal("expected sensitive path to be rejected")
	}
}

func TestReplaceRequiresExactlyOneMatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.go")
	if err := os.WriteFile(path, []byte("one\ntwo\none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReplaceTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"app.go","old_text":"one","new_text":"three"}`), registry.Runtime{})
	if err == nil || !strings.Contains(err.Error(), "matched 2 times") {
		t.Fatalf("error = %v, want duplicate match error", err)
	}
}

func TestReplaceUpdatesSingleMatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.go")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReplaceTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"app.go","old_text":"two","new_text":"three"}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "replaced text") {
		t.Fatalf("content = %q", result.Content)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one\nthree\n" {
		t.Fatalf("file content = %q", got)
	}
}

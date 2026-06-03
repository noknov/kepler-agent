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

func TestReadFileRequiresConfirmationForSensitiveLocalPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	raw := json.RawMessage(`{"path":".env"}`)

	result, err := tool.Execute(context.Background(), raw, registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.WaitForUser || result.PendingActionKey == "" {
		t.Fatalf("expected pending confirmation, got %#v", result)
	}

	result, err = tool.Execute(context.Background(), raw, registry.Runtime{
		ConfirmedActions: map[string]bool{result.PendingActionKey: true},
	})
	if err != nil {
		t.Fatalf("confirmed Execute() error = %v", err)
	}
	if result.WaitForUser || result.Content == "" {
		t.Fatalf("expected file content after confirmation, got %#v", result)
	}
}

func TestSearchRequiresConfirmationForSensitiveQuery(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"token"}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.WaitForUser || result.PendingActionKey == "" {
		t.Fatalf("expected pending confirmation, got %#v", result)
	}
}

package localtools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

func TestCanceledFileMutationsLeaveWorkspaceUnchanged(t *testing.T) {
	for _, operation := range []string{"write", "edit"} {
		t.Run(operation, func(t *testing.T) {
			workspace, err := local.NewWorkspace(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			path := filepath.Join(workspace.Root, "source.txt")
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			var item tool.Tool = WriteFile{Workspace: workspace}
			args := `{"path":"source.txt","content":"changed"}`
			if operation == "edit" {
				item = EditFile{Workspace: workspace}
				args = `{"path":"source.txt","old_text":"original","new_text":"changed"}`
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err = item.Execute(ctx, tool.Call{Arguments: json.RawMessage(args)})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("canceled mutation returned %v", err)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || string(data) != "original" {
				t.Fatalf("canceled mutation changed source: %q %v", data, readErr)
			}
		})
	}
}

func TestWriteFilePreservesExistingPrivatePermissions(t *testing.T) {
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	path := filepath.Join(workspace.Root, "private.txt")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = (WriteFile{Workspace: workspace}).Execute(t.Context(), tool.Call{Arguments: json.RawMessage(`{"path":"private.txt","content":"changed"}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private file permissions changed: %v %v", info, err)
	}
}

func TestCanceledFileCreationDoesNotCreateDirectories(t *testing.T) {
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = (WriteFile{Workspace: workspace}).Execute(ctx, tool.Call{Arguments: json.RawMessage(`{"path":"new/sub/file.txt","content":"changed"}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled creation returned %v", err)
	}
	entries, err := os.ReadDir(workspace.Root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled creation changed workspace: %v %v", entries, err)
	}
}

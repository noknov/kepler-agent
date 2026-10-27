//go:build unix

package localtools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

func TestFileToolsRejectNamedPipesWithoutBlocking(t *testing.T) {
	if root := os.Getenv("KEPLER_TEST_FILE_PIPE_ROOT"); root != "" {
		workspace, err := local.NewWorkspace(root)
		if err != nil {
			t.Fatal(err)
		}
		defer workspace.Close()
		var item tool.Tool = ReadFile{Workspace: workspace}
		args := json.RawMessage(`{"path":"pipe"}`)
		if os.Getenv("KEPLER_TEST_FILE_PIPE_OPERATION") == "edit" {
			item = EditFile{Workspace: workspace}
			args = json.RawMessage(`{"path":"pipe","old_text":"old","new_text":"new"}`)
		}
		if _, err := item.Execute(t.Context(), tool.Call{Arguments: args}); err == nil {
			t.Fatal("accepted a pipe as a file")
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"read", "edit"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
				t.Skip(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, executable, "-test.run=^TestFileToolsRejectNamedPipesWithoutBlocking$")
			child.Env = append(os.Environ(), "KEPLER_TEST_FILE_PIPE_ROOT="+root, "KEPLER_TEST_FILE_PIPE_OPERATION="+operation)
			output, err := child.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("%s tool blocked opening a named pipe", operation)
			}
			if err != nil {
				t.Fatalf("child failed: %v\n%s", err, output)
			}
		})
	}
}

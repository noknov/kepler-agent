package localtools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

func TestReadFileReportsTruncatedMemberInBatch(t *testing.T) {
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	for name, body := range map[string]string{"a.txt": "abcdef", "b.txt": "xy"} {
		if err := os.WriteFile(filepath.Join(workspace.Root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := (ReadFile{Workspace: workspace}).Execute(t.Context(), tool.Call{Arguments: json.RawMessage(`{"paths":["a.txt","b.txt"],"limit":3}`)})
	if err != nil || !result.Truncated || !strings.Contains(result.Text(), "## a.txt\nabc") || !strings.Contains(result.Text(), "## b.txt\nxy") {
		t.Fatalf("batch lost truncated evidence: %+v err=%v", result, err)
	}
}

func TestReadFileRejectsUnboundedRequestedReads(t *testing.T) {
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	if err := os.WriteFile(filepath.Join(workspace.Root, "file.txt"), []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		args    map[string]any
		message string
	}{
		{"integer overflow", map[string]any{"path": "file.txt", "limit": int(^uint(0) >> 1)}, "limit"},
		{"negative limit", map[string]any{"path": "file.txt", "limit": -1}, "limit"},
		{"aggregate budget", map[string]any{"paths": []string{"file.txt", "missing.txt"}, "limit": 16 << 20}, "budget"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args, _ := json.Marshal(test.args)
			result, err := (ReadFile{Workspace: workspace}).Execute(t.Context(), tool.Call{Arguments: args})
			if err == nil || !strings.Contains(err.Error(), test.message) || result.Text() != "" {
				t.Fatalf("accepted invalid requested read: %+v err=%v", result, err)
			}
		})
	}
}

func TestReadFileHonorsCancellationBeforeReading(t *testing.T) {
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	if err := os.WriteFile(filepath.Join(workspace.Root, "file.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := (ReadFile{Workspace: workspace}).Execute(ctx, tool.Call{Arguments: json.RawMessage(`{"path":"file.txt"}`)})
	if !errors.Is(err, context.Canceled) || result.Text() != "" {
		t.Fatalf("canceled read published content: %+v err=%v", result, err)
	}
}

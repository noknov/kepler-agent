package edit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/safety"
)

func TestFileToolsSkipUnavailableUnrelatedRoots(t *testing.T) {
	for _, operation := range []string{"write", "replace"} {
		t.Run(operation, func(t *testing.T) {
			missing := filepath.Join(t.TempDir(), "absent")
			live := t.TempDir()
			target := filepath.Join(live, "file.txt")
			if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			policy := safety.WorkspacePolicy{Roots: []string{missing, live}}
			args := map[string]string{"path": target, "content": "updated"}
			var item tool.Tool = WriteFileTool{Paths: policy}
			if operation == "replace" {
				item = ReplaceTool{Paths: policy}
				args = map[string]string{"path": target, "old_text": "original", "new_text": "updated"}
			}
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := item.Execute(t.Context(), tool.Call{Arguments: raw}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != "updated" {
				t.Fatalf("usable root rejected: %q %v", got, err)
			}
			if _, err := os.Stat(missing); !os.IsNotExist(err) {
				t.Fatalf("unrelated root was created: %v", err)
			}
		})
	}
}

func TestWriteDoesNotRetargetAnExplicitUnavailableRoot(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		t.Run(fmt.Sprintf("absolute=%t", absolute), func(t *testing.T) {
			missing := filepath.Join(t.TempDir(), "removed-root")
			live := t.TempDir()
			path := "removed-root/new.txt"
			if absolute {
				path = filepath.Join(missing, "new.txt")
			}
			raw, _ := json.Marshal(map[string]string{"path": path, "content": "wrong target"})
			_, err := (WriteFileTool{Paths: safety.WorkspacePolicy{Roots: []string{missing, live}}}).Execute(t.Context(), tool.Call{Arguments: raw})
			if err == nil {
				t.Fatal("retargeted explicitly selected missing root")
			}
			entries, err := os.ReadDir(live)
			if err != nil || len(entries) != 0 {
				t.Fatalf("unrelated root modified: %v %v", entries, err)
			}
		})
	}
}

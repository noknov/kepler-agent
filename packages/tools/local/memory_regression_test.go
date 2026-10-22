package localtools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

func TestMemoryEffectsAndProjectIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	first := MemoryTool{Workspace: local.Workspace{Root: "/project/first"}, StorePath: path}
	second := MemoryTool{Workspace: local.Workspace{Root: "/project/second"}, StorePath: path}
	write := tool.Call{Arguments: json.RawMessage(`{"action":"upsert","semantic_key":"build","content":"private-first-project"}`)}
	d, err := tool.DescribeCall(first, write)
	if err != nil || d.Parallel || d.Effects[0] != tool.EffectExternalWrite {
		t.Fatalf("write effect=%+v err=%v", d, err)
	}
	if _, err := first.Execute(context.Background(), write); err != nil {
		t.Fatal(err)
	}
	read := tool.Call{Arguments: json.RawMessage(`{"action":"list"}`)}
	d, err = tool.DescribeCall(first, read)
	if err != nil || !d.Parallel || d.Effects[0] != tool.EffectRead {
		t.Fatalf("read effect=%+v err=%v", d, err)
	}
	result, err := second.Execute(context.Background(), read)
	if err != nil || strings.Contains(result.Text(), "private-first-project") {
		t.Fatalf("cross-project leak: %s %v", result.Text(), err)
	}
}

func TestMemoryFileLockCoordinatesProcesses(t *testing.T) {
	// Spawn this test executable to exercise OS locking, not an in-process mutex.
	if path := os.Getenv("KEPLER_TEST_MEMORY_LOCK"); path != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancel()
		unlock, err := lockMemoryFile(ctx, path)
		if err == nil {
			unlock()
			t.Fatal("child acquired parent's memory lock")
		}
		if ctx.Err() == nil {
			t.Fatal(err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "memory.lock")
	unlock, err := lockMemoryFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	executable, err := os.Executable()
	if err != nil {
		t.Skipf("test executable cannot be resolved in this environment: %v", err)
	}
	command := exec.Command(executable, "-test.run=^TestMemoryFileLockCoordinatesProcesses$")
	command.Env = append(os.Environ(), "KEPLER_TEST_MEMORY_LOCK="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("child: %s %v", output, err)
	}
}

func TestReadFileUTF8BoundaryDoesNotRejectValidChinese(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "utf8.txt")
	if err := os.WriteFile(path, []byte("abc你好xyz"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := ReadFile{Workspace: local.Workspace{Root: root}}
	text, truncated, err := reader.readOne("utf8.txt", 0, 7)
	if err != nil || !truncated || text != "abc你" {
		t.Fatalf("read=%q truncated=%v err=%v", text, truncated, err)
	}
	if err := os.WriteFile(path, []byte{'a', 0xff, 'b', 'c', 'd', 'e', 'f', 'g'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.readOne("utf8.txt", 0, 7); err == nil {
		t.Fatal("malformed interior UTF-8 was silently removed")
	}
}

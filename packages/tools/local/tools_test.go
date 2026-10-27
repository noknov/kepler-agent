package localtools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

func TestFileToolsAndCatalog(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := local.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	write := WriteFile{Workspace: workspace}
	if _, err := write.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"path":"a.txt","content":"hello"}`)}); err != nil {
		t.Fatal(err)
	}
	read, err := (ReadFile{Workspace: workspace}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"path":"a.txt"}`)})
	if err != nil || read.Text() != "hello" {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	if string(data(t, root+"/a.txt")) != "hello" {
		t.Fatal("file was not written")
	}
	catalog, err := NewCatalog(workspace, local.Sandbox{Workspace: workspace, UnsafeAllowNoSandbox: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.Get("exec"); !ok {
		t.Fatal("exec missing")
	}
	if _, ok := catalog.Get("tool_search"); ok {
		t.Fatal("local catalog registered tool_search without any deferred tools")
	}
	listed, err := (ListFiles{Workspace: workspace}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(listed.Text(), ".env") {
		t.Fatalf("sensitive filename leaked: %s", listed.Text())
	}
}

func TestSearchAcceptsFilePathAndExcludesCredentialFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".npmrc"), []byte("needle token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := local.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	search := Search{Workspace: workspace}
	result, err := search.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"needle","path":"notes.txt"}`)})
	if err != nil || !strings.Contains(result.Text(), "needle") {
		t.Fatalf("result=%q err=%v", result.Text(), err)
	}
	result, err = search.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"needle"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Text(), ".npmrc") || strings.Contains(result.Text(), "token") {
		t.Fatalf("credential search leaked: %q", result.Text())
	}
}

func data(t *testing.T, path string) []byte {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestExactEditFileAndMemoryTool(t *testing.T) {
	root := t.TempDir()
	workspace, err := local.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	target := filepath.Join(root, "main.go")
	if err := os.WriteFile(target, []byte("package main\n\tconst x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := EditFile{Workspace: workspace}
	if _, err := edit.Execute(context.Background(), agenttool.Call{
		Arguments: json.RawMessage(`{"path":"main.go","old_text":"package main\n\tconst x = 1","new_text":"package main\n\tconst x = 2"}`),
	}); err != nil {
		t.Fatalf("EditFile failed: %v", err)
	}
	if !strings.Contains(string(data(t, target)), "const x = 2") {
		t.Fatalf("unexpected file content: %s", string(data(t, target)))
	}

	memStore := filepath.Join(root, "memories.json")
	mem := MemoryTool{Workspace: workspace, StorePath: memStore}
	if _, err := mem.Execute(context.Background(), agenttool.Call{
		Arguments: json.RawMessage(`{"action":"upsert","semantic_key":"code.style","kind":"preference","content":"Prefer table-driven Go tests"}`),
		Scope:     agenttool.Scope{SessionID: "s1", TurnID: "t1"},
	}); err != nil {
		t.Fatalf("memory upsert: %v", err)
	}
	listed, err := mem.Execute(context.Background(), agenttool.Call{
		Arguments: json.RawMessage(`{"action":"search","query":"table-driven"}`),
	})
	if err != nil || !strings.Contains(listed.Text(), "code.style") {
		t.Fatalf("memory search=%q err=%v", listed.Text(), err)
	}
}

func TestLocalEditRejectsNonExactWhitespaceWithoutChangingFile(t *testing.T) {
	for _, test := range []struct {
		name, content, old string
		all                bool
	}{
		{"indentation", "if enabled:\n    run()\n", "if enabled:\nrun()", false},
		{"line endings", "header\nalpha\r\nbeta\r\nfooter\n", "alpha\nbeta", false},
		{"trailing whitespace", "alpha  \nbeta\n", "alpha\nbeta", false},
		{"overlapping fuzzy spans", " a\n a\n a\n", "a\na", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace, err := local.NewWorkspace(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			path := filepath.Join(workspace.Root, "example.txt")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			args, _ := json.Marshal(map[string]any{"path": "example.txt", "old_text": test.old, "new_text": "changed", "replace_all": test.all})
			_, err = (EditFile{Workspace: workspace}).Execute(t.Context(), agenttool.Call{Arguments: args})
			if err == nil {
				t.Error("accepted non-exact old_text")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != test.content {
				t.Fatalf("non-exact edit changed bytes: %q err=%v", got, err)
			}
		})
	}
}

func TestLocalExactEditPreservesUnrelatedBytesAndExplicitReplaceAll(t *testing.T) {
	for _, test := range []struct {
		name, content, old, replacement, want string
		all                                   bool
	}{
		{"mixed endings", "header\nalpha\r\nbeta\r\nfooter\n", "alpha\r\nbeta", "changed", "header\nchanged\r\nfooter\n", false},
		{"replace all", "one\none\n", "one", "two", "two\ntwo\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace, err := local.NewWorkspace(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			path := filepath.Join(workspace.Root, "example.txt")
			if err := os.WriteFile(path, []byte(test.content), 0o755); err != nil {
				t.Fatal(err)
			}
			args, _ := json.Marshal(map[string]any{"path": "example.txt", "old_text": test.old, "new_text": test.replacement, "replace_all": test.all})
			if _, err := (EditFile{Workspace: workspace}).Execute(t.Context(), agenttool.Call{Arguments: args}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != test.want {
				t.Fatalf("wrong exact replacement: %q err=%v", got, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("mode changed: %v %v", info, err)
			}
		})
	}
}

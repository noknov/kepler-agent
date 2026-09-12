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

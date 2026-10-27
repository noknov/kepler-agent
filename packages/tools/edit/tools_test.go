package edit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/safety"
)

func TestWriteFileCreatesWorkspaceFile(t *testing.T) {
	root := t.TempDir()
	tool := WriteFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}

	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"path":"src/app.go","content":"package app\n"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Text(), "wrote src/app.go") {
		t.Fatalf("content = %q", result.Text())
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

	_, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"path":"repo/main.go","content":"package main\n"}`), Scope: agenttool.Scope{}})
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

	_, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"path":"../escape.go","content":"package escape\n"}`), Scope: agenttool.Scope{}})
	if err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func TestWriteFileRejectsSensitiveFile(t *testing.T) {
	root := t.TempDir()
	tool := WriteFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}

	_, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"path":".env","content":"TOKEN=x\n"}`), Scope: agenttool.Scope{}})
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

	_, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"path":"app.go","old_text":"one","new_text":"three"}`), Scope: agenttool.Scope{}})
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

	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"path":"app.go","old_text":"two","new_text":"three"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Text(), "replaced text") {
		t.Fatalf("content = %q", result.Text())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one\nthree\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestReplaceRejectsIndentationMismatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.go")
	if err := os.WriteFile(path, []byte("func main() {\n\tprintln(\"hello\")\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReplaceTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}

	_, err := tool.Execute(context.Background(), agenttool.Call{
		Arguments: json.RawMessage(`{"path":"app.go","old_text":"func main() {\n    println(\"hello\")\n}","new_text":"func main() {\n\tprintln(\"world\")\n}"}`),
	})
	if err == nil {
		t.Fatal("accepted indentation mismatch")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "func main() {\n\tprintln(\"hello\")\n}\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestWriteFileCannotFollowWorkspaceSymlinksOutsideRoots(t *testing.T) {
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			target := filepath.Join(outside, "target.txt")
			if err := os.WriteFile(target, []byte("outside original"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "alias")
			path := "alias"
			source := target
			if kind == "directory" {
				source = outside
				path = "alias/target.txt"
			}
			if err := os.Symlink(source, link); err != nil {
				t.Skip(err)
			}
			args, _ := json.Marshal(map[string]string{"path": path, "content": "overwritten"})
			_, err := (WriteFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}).Execute(t.Context(), agenttool.Call{Arguments: args})
			if err == nil {
				t.Error("accepted write outside configured roots")
			}
			got, readErr := os.ReadFile(target)
			if readErr != nil || string(got) != "outside original" {
				t.Fatalf("outside file changed: %q err=%v", got, readErr)
			}
		})
	}
}

func TestWorkspaceFileUpdatesDoNotMutateExternalHardLinks(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprintf("replace=%t", replace), func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			target := filepath.Join(outside, "original.txt")
			if err := os.WriteFile(target, []byte("original"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(target, filepath.Join(root, "local.txt")); err != nil {
				t.Skip(err)
			}
			policy := safety.WorkspacePolicy{Roots: []string{root}}
			var item agenttool.Tool = WriteFileTool{Paths: policy}
			args := json.RawMessage(`{"path":"local.txt","content":"updated"}`)
			if replace {
				item = ReplaceTool{Paths: policy}
				args = json.RawMessage(`{"path":"local.txt","old_text":"original","new_text":"updated"}`)
			}
			if _, err := item.Execute(t.Context(), agenttool.Call{Arguments: args}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != "original" {
				t.Fatalf("external hard link mutated: %q err=%v", got, err)
			}
			got, err = os.ReadFile(filepath.Join(root, "local.txt"))
			if err != nil || string(got) != "updated" {
				t.Fatalf("local update missing: %q err=%v", got, err)
			}
			info, err := os.Stat(filepath.Join(root, "local.txt"))
			if err != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("mode was not preserved: %v err=%v", info, err)
			}
		})
	}
}

func TestWriteFileRejectsSensitiveSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, ".env")
	if err := os.WriteFile(secret, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "alias.txt")); err != nil {
		t.Skip(err)
	}
	_, err := (WriteFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}).Execute(t.Context(), agenttool.Call{Arguments: json.RawMessage(`{"path":"alias.txt","content":"updated"}`)})
	if err == nil {
		t.Fatal("accepted sensitive target through alias")
	}
	got, err := os.ReadFile(secret)
	if err != nil || string(got) != "original" {
		t.Fatalf("sensitive target changed: %q %v", got, err)
	}
}

func TestWorkspaceWriteRejectsParentSwapAfterValidation(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "parent"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "file.txt")
	if err := os.WriteFile(target, []byte("outside original"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := openWorkspaceFile(safety.WorkspacePolicy{Roots: []string{root}}, "parent/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.root.Close()
	if err := os.Rename(filepath.Join(root, "parent"), filepath.Join(root, "original-parent")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "parent")); err != nil {
		t.Skip(err)
	}
	if err := file.write(t.Context(), []byte("updated")); err == nil {
		t.Fatal("write escaped after validation")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "outside original" {
		t.Fatalf("outside target changed: %q %v", got, err)
	}
}

func TestWriteFileAllowsSymlinkIntoAnotherExplicitRoot(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	if err := os.Symlink(second, filepath.Join(first, "shared")); err != nil {
		t.Skip(err)
	}
	_, err := (WriteFileTool{Paths: safety.WorkspacePolicy{Roots: []string{first, second}}}).Execute(t.Context(), agenttool.Call{Arguments: json.RawMessage(`{"path":"shared/new.txt","content":"allowed"}`)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(second, "new.txt"))
	if err != nil || string(got) != "allowed" {
		t.Fatalf("explicit root not writable: %q %v", got, err)
	}
}

func TestReplaceRejectsNonExactWhitespaceWithoutChangingFile(t *testing.T) {
	for _, test := range []struct{ name, content, old string }{
		{"indentation", "if enabled:\n    run()\n", "if enabled:\nrun()"},
		{"line endings", "header\nalpha\r\nbeta\r\nfooter\n", "alpha\nbeta"},
		{"trailing whitespace", "alpha  \nbeta\n", "alpha\nbeta"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "example.txt")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			args, _ := json.Marshal(map[string]string{"path": "example.txt", "old_text": test.old, "new_text": "changed"})
			_, err := (ReplaceTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}).Execute(t.Context(), agenttool.Call{Arguments: args})
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

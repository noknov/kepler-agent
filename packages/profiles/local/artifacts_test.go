package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

func TestArtifactEvidenceIsReadableWithoutWorkspaceAccess(t *testing.T) {
	root := t.TempDir()
	store := ArtifactStore{Root: root}
	scope := tool.Scope{SessionID: "session", TurnID: "turn1"}
	first, err := store.Put(context.Background(), scope, "read-call.json", []byte("甲乙丙"))
	if err != nil {
		t.Fatal(err)
	}
	scope.TurnID = "turn2"
	second, err := store.Put(context.Background(), scope, "read-call.json", []byte("later"))
	if err != nil {
		t.Fatal(err)
	}
	if first.URI == second.URI || strings.Contains(first.URI, root) {
		t.Fatal("artifact identity leaks a path or collides across turns")
	}
	reader := ArtifactReadTool{Root: root}
	read := func(uri string, offset int64, limit int, scope tool.Scope) (tool.Result, error) {
		args, _ := json.Marshal(map[string]any{"uri": uri, "offset": offset, "limit": limit})
		return reader.Execute(context.Background(), tool.Call{Scope: scope, Arguments: args})
	}
	got, err := read(first.URI, 0, 3, scope)
	if err != nil || !strings.HasSuffix(got.Text(), "甲") || !strings.Contains(got.Text(), "next_offset=3") {
		t.Fatalf("first page=%q err=%v", got.Text(), err)
	}
	got, err = read(first.URI, 1, 3, scope)
	if err != nil || !utf8.ValidString(got.Text()) || !strings.HasSuffix(got.Text(), "乙") {
		t.Fatalf("rune boundary=%q err=%v", got.Text(), err)
	}
	got, err = read(first.URI, 9, 3, scope)
	if err != nil || !strings.Contains(got.Text(), "eof=true") {
		t.Fatalf("eof=%q err=%v", got.Text(), err)
	}
	for _, uri := range []string{first.URI, "artifact://other/" + first.ID, "artifact://session/../../credentials.json", filepath.Join(root, "session", "artifacts", first.ID)} {
		other := scope
		other.SessionID = "other"
		if _, err := read(uri, 0, 3, other); err == nil {
			t.Fatalf("cross-session/path read accepted: %q", uri)
		}
	}
	// Even a syntactically valid ID may not follow a symlink out of artifacts.
	if err := os.Remove(filepath.Join(root, "session", "artifacts", first.ID)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "private"), filepath.Join(root, "session", "artifacts", first.ID)); err != nil {
		t.Skip(err)
	}
	if _, err := read(first.URI, 0, 3, scope); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

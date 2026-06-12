package indexer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseNameStatus(t *testing.T) {
	files := parseNameStatus("M\tcmd/main.go\nD\told.go\nR100\tbefore.go\tafter.go\n")
	if len(files) != 4 {
		t.Fatalf("expected 4 changed file entries, got %d: %#v", len(files), files)
	}

	check := func(i int, path string, deleted bool) {
		t.Helper()
		if files[i].Path != path || files[i].Deleted != deleted {
			t.Fatalf("files[%d] = %#v, want path=%q deleted=%v", i, files[i], path, deleted)
		}
	}

	check(0, "cmd/main.go", false)
	check(1, "old.go", true)
	check(2, "before.go", true)
	check(3, "after.go", false)
}

func TestFilterIndexableKeepsDeletedSourceFiles(t *testing.T) {
	files := filterIndexable([]changedFile{
		{Path: "old.go", Deleted: true},
		{Path: "image.png", Deleted: true},
		{Path: "README.md"},
	})

	if len(files) != 2 {
		t.Fatalf("expected 2 indexable files, got %d: %#v", len(files), files)
	}
	if files[0].Path != "old.go" || !files[0].Deleted {
		t.Fatalf("first file = %#v, want deleted old.go", files[0])
	}
	if files[1].Path != "README.md" || files[1].Deleted {
		t.Fatalf("second file = %#v, want live README.md", files[1])
	}
}

func TestResolveBranchRefFallsBackToLocalBranch(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "init")

	ref, err := resolveBranchRef(context.Background(), repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "main" {
		t.Fatalf("ref = %q, want main", ref)
	}
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

//go:build unix

package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationOverlappingReadRootsPreserveWorkspaceAuthority(t *testing.T) {
	if os.Getenv("SANDBOX_INTEGRATION") != "1" {
		t.Skip("SANDBOX_INTEGRATION is not enabled")
	}
	for _, location := range []string{"workspace", "ancestor"} {
		t.Run(location, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "workspace")
			if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
			const secret = "synthetic-git-credential"
			if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte(secret), 0o600); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(parent, "read-only.txt")
			if err := os.WriteFile(outside, []byte("readable"), 0o600); err != nil {
				t.Fatal(err)
			}
			workspace, err := NewWorkspace(root)
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			readRoot := root
			if location == "ancestor" {
				readRoot = parent
			}
			sandbox := Sandbox{Workspace: workspace, AdditionalReadRoots: []string{readRoot}}
			hidden, err := sandbox.Run(t.Context(), CommandRequest{Network: true, Argv: []string{"/bin/cat", filepath.Join(root, ".git", "config")}})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(hidden.Output, secret) {
				t.Fatalf("read root exposed masked credential: %+v", hidden)
			}
			write, err := sandbox.Run(t.Context(), CommandRequest{Network: true, Argv: []string{"/bin/sh", "-c", `printf owned > owned.txt; printf scratch > "$TMPDIR/scratch"; cat owned.txt "$TMPDIR/scratch"`}})
			if err != nil || write.ExitCode != 0 || write.Output != "ownedscratch" {
				t.Fatalf("owned paths lost write access: %+v %v", write, err)
			}
			if location == "ancestor" {
				read, err := sandbox.Run(t.Context(), CommandRequest{Network: true, Argv: []string{"/bin/cat", outside}})
				if err != nil || read.ExitCode != 0 || read.Output != "readable" {
					t.Fatalf("read root inaccessible: %+v %v", read, err)
				}
				_, err = sandbox.Run(t.Context(), CommandRequest{Network: true, Argv: []string{"/bin/sh", "-c", `printf changed > "$1"`, "script", outside}})
				if err != nil {
					t.Fatal(err)
				}
				actual, err := os.ReadFile(outside)
				if err != nil || string(actual) != "readable" {
					t.Fatalf("read root allowed mutation: %q %v", actual, err)
				}
			}
		})
	}
}

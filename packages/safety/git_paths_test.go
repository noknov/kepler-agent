package safety

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommittedTreeUsesFilesystemSensitivePathPolicy(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return string(out)
	}
	git("init", "-q")
	paths := []string{".aws", "sub/.kube", ".env", "sub/.ENV.production", "id_rsa", "sub/id_ed25519", "credentials.json", "sub/CREDENTIALS.json", "sub/client.KUBECONFIG", "sub/key.PEM", "sub/.AWS/config", "secrets/nested/data.txt", "sub/credentials/anything", "sub/.docker/config.json", "sub/.config/gh/hosts.yml"}
	for _, path := range append(paths, "public.txt") {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("sentinel"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-f", ".")
	// git grep --cached exercises real Git pathspec matching, including root
	// files, nested files, mixed case and excluded credential directories.
	args := append([]string{"grep", "--cached", "-l", "sentinel", "--", "."}, SensitiveGitPathspecs()...)
	if got := strings.TrimSpace(git(args...)); got != "public.txt" {
		t.Fatalf("credential paths escaped exclusions: %s", got)
	}
	for _, path := range paths {
		if !IsSensitivePath(path) {
			t.Fatalf("filesystem permits %s", path)
		}
	}
}

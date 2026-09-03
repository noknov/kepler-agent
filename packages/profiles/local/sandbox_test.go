package local

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSafeEnvironmentDoesNotInheritCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("PATH", "/bin")
	environment := safeEnvironment("/isolated", nil)
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "must-not-leak") || strings.Contains(joined, "OPENAI_API_KEY") {
		t.Fatalf("credential leaked: %s", joined)
	}
	if !strings.Contains(joined, "HOME=/isolated") || !strings.Contains(joined, "PATH=/bin") {
		t.Fatalf("missing safe values: %s", joined)
	}
}

func TestSafeEnvironmentRejectsDangerousOverrides(t *testing.T) {
	environment := safeEnvironment("/isolated", []string{"PATH=/attacker", "HOME=/attacker", "LD_PRELOAD=/evil", "LANG=zh_CN.UTF-8"})
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "/attacker") || strings.Contains(joined, "LD_PRELOAD") {
		t.Fatalf("dangerous environment override accepted: %s", joined)
	}
	if !strings.Contains(joined, "LANG=zh_CN.UTF-8") {
		t.Fatalf("safe locale override missing: %s", joined)
	}
}

func TestDarwinSandboxProfileAllowsDevNullWrites(t *testing.T) {
	workspace := Workspace{Root: "/workspace", Temp: "/workspace/.kepler-tmp"}
	profile := darwinSandboxProfile(workspace, nil, nil, false)
	if !strings.Contains(profile, `(allow file-write* (literal "/dev/null"))`) {
		t.Fatalf("/dev/null write access is required for standard command redirection: %s", profile)
	}
	if !strings.Contains(profile, "(deny file-write*)") {
		t.Fatalf("sandbox must keep its default write denial: %s", profile)
	}
}

func TestCanonicalReadRootsRejectsBroadAndResolvesSymlink(t *testing.T) {
	if _, err := canonicalReadRoots([]string{"/"}); err == nil {
		t.Fatal("root read grant was accepted")
	}
	dir := t.TempDir()
	link := dir + "-link"
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(link)
	roots, err := canonicalReadRoots([]string{link, dir})
	real, _ := filepath.EvalSymlinks(dir)
	if err != nil || len(roots) != 1 || roots[0] != real {
		t.Fatalf("roots=%#v err=%v", roots, err)
	}
}

func TestIntegrationSandboxHidesWorkspaceCredentials(t *testing.T) {
	if os.Getenv("SANDBOX_INTEGRATION") == "" {
		t.Skip("SANDBOX_INTEGRATION is not set")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("sandbox is not supported on this OS")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	const secret = "embedded-token-must-not-leak"
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	result, err := (Sandbox{Workspace: workspace}).Run(context.Background(), CommandRequest{Argv: []string{"cat", ".git/config"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == 0 || strings.Contains(result.Output, secret) {
		t.Fatalf("credential read escaped sandbox: %+v", result)
	}
}

func TestIntegrationSandboxAllowsDevNull(t *testing.T) {
	if os.Getenv("SANDBOX_INTEGRATION") == "" || runtime.GOOS != "darwin" {
		t.Skip("macOS sandbox integration test is disabled")
	}
	workspace, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	result, err := (Sandbox{Workspace: workspace}).Run(context.Background(), CommandRequest{
		Argv: []string{"/bin/sh", "-c", "printf ok >/dev/null"},
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("/dev/null redirection failed: result=%+v err=%v", result, err)
	}
}

func TestIntegrationSandboxRunsGitShow(t *testing.T) {
	if os.Getenv("SANDBOX_INTEGRATION") == "" || runtime.GOOS != "darwin" {
		t.Skip("macOS sandbox integration test is disabled")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", root},
		{"-C", root, "-c", "user.name=Kepler Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "initial"},
		{"-C", root, "remote", "add", "origin", "https://embedded-token@example.invalid/repo.git"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v: %s", args, err, output)
		}
	}
	workspace, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	result, err := (Sandbox{Workspace: workspace}).Run(context.Background(), CommandRequest{
		Argv: []string{"git", "show", "--oneline", "--no-patch", "HEAD"},
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("git show failed: result=%+v err=%v", result, err)
	}
	configResult, err := (Sandbox{Workspace: workspace}).Run(context.Background(), CommandRequest{
		Argv: []string{"git", "config", "--get", "remote.origin.url"},
	})
	if err != nil || configResult.ExitCode == 0 || strings.Contains(configResult.Output, "embedded-token") {
		t.Fatalf("sandboxed Git exposed repository credentials: result=%+v err=%v", configResult, err)
	}
}

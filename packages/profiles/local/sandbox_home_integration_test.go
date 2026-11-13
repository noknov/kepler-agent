//go:build unix

package local

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationSandboxHidesAccountHomeWhenHOMEIsOverridden(t *testing.T) {
	if os.Getenv("SANDBOX_INTEGRATION") != "1" {
		t.Skip("SANDBOX_INTEGRATION is not enabled")
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.MkdirTemp(account.HomeDir, ".kepler-sandbox-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(fixture)
	target := filepath.Join(fixture, "synthetic-private-content.txt")
	if err := os.WriteFile(target, []byte("account-home-fixture-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	t.Setenv("HOME", workspace.Temp)
	result, err := (Sandbox{Workspace: workspace}).Run(t.Context(), CommandRequest{Argv: []string{"/bin/cat", target}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == 0 || strings.Contains(result.Output, "account-home-fixture-only") {
		t.Fatalf("overridden HOME exposed the OS account home: %+v", result)
	}
}

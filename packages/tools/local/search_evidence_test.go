package localtools

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

func TestSearchDoesNotSilentlyStopAtFiftyMatchesPerFile(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is unavailable")
	}
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	if err := os.WriteFile(filepath.Join(workspace.Root, "matches.txt"), []byte(strings.Repeat("needle\n", 80)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		limit, count int
		truncated    bool
	}{
		{100, 80, false}, {80, 80, false}, {79, 79, true}, {1, 1, true}, {math.MaxInt, 80, false},
	} {
		t.Run(fmt.Sprint(tc.limit), func(t *testing.T) {
			arguments, _ := json.Marshal(map[string]any{"query": "needle", "limit": tc.limit})
			result, err := (Search{Workspace: workspace}).Execute(t.Context(), tool.Call{Arguments: arguments})
			if err != nil || strings.Count(result.Text(), "needle") != tc.count || result.Truncated != tc.truncated {
				t.Fatalf("search lost matches: count=%d truncated=%v err=%v", strings.Count(result.Text(), "needle"), result.Truncated, err)
			}
		})
	}
}

func TestSearchReportsByteBudgetTruncation(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is unavailable")
	}
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	content := []byte(strings.Repeat("needle "+strings.Repeat("x", 350)+"\n", 50))
	for i := 0; i < 240; i++ {
		if err := os.WriteFile(filepath.Join(workspace.Root, fmt.Sprintf("file-%03d.txt", i)), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := (Search{Workspace: workspace}).Execute(t.Context(), tool.Call{Arguments: json.RawMessage(`{"query":"needle","limit":30000}`)})
	if err != nil || !result.Truncated || result.Text() == "" || len(result.Text()) > 4<<20 {
		t.Fatalf("byte-limited search looked complete: bytes=%d truncated=%v err=%v", len(result.Text()), result.Truncated, err)
	}
}

func TestFileDiscoverySharesSensitivePathPolicy(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is unavailable")
	}
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	if err := os.WriteFile(filepath.Join(workspace.Root, "visible.txt"), []byte("needle-visible"), 0o600); err != nil {
		t.Fatal(err)
	}
	sensitive := []string{".ENV", "ID_RSA", "credentials.JSON", "profile.KUBECONFIG", "nested/SECRETs/data.txt", ".aws"}
	for _, name := range sensitive {
		path := filepath.Join(workspace.Root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("needle-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.Resolve(name, false); err == nil {
			t.Fatalf("fixture is not protected by direct reads: %s", name)
		}
	}
	result, err := (Search{Workspace: workspace}).Execute(t.Context(), tool.Call{Arguments: json.RawMessage(`{"query":"needle"}`)})
	if err != nil || strings.Contains(result.Text(), "needle-secret") || !strings.Contains(result.Text(), "needle-visible") {
		t.Fatalf("search bypassed read policy: %q %v", result.Text(), err)
	}
	result, err = (ListFiles{Workspace: workspace}).Execute(t.Context(), tool.Call{Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range sensitive {
		if strings.Contains(result.Text(), name) {
			t.Errorf("list exposed protected path: %s", name)
		}
	}
}

func TestFileDiscoveryIgnoresAmbientRipgrepConfiguration(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is unavailable")
	}
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace.Root, "local.txt"), []byte("needle-local"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "external.txt"), []byte("needle-outside-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace.Root, "linked")); err != nil {
		t.Skip(err)
	}
	config := filepath.Join(t.TempDir(), "rgconfig")
	if err := os.WriteFile(config, []byte("--follow\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIPGREP_CONFIG_PATH", config)
	result, err := (Search{Workspace: workspace}).Execute(t.Context(), tool.Call{Arguments: json.RawMessage(`{"query":"needle"}`)})
	if err != nil || strings.Contains(result.Text(), "outside-only") || !strings.Contains(result.Text(), "needle-local") {
		t.Fatalf("ambient config changed read boundary: %q %v", result.Text(), err)
	}
	result, err = (ListFiles{Workspace: workspace}).Execute(t.Context(), tool.Call{Arguments: json.RawMessage(`{}`)})
	if err != nil || strings.Contains(result.Text(), "external.txt") {
		t.Fatalf("ambient config changed list boundary: %q %v", result.Text(), err)
	}
}

func TestSearchPreservesLongMatchingLinesWithinByteBudget(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is unavailable")
	}
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	line := "needle " + strings.Repeat("x", 600)
	if err := os.WriteFile(filepath.Join(workspace.Root, "long.txt"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (Search{Workspace: workspace}).Execute(t.Context(), tool.Call{Arguments: json.RawMessage(`{"query":"needle"}`)})
	if err != nil || result.Truncated || !strings.Contains(result.Text(), line) {
		t.Fatalf("matching line was silently shortened: %q %v", result.Text(), err)
	}
}

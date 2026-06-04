package codeintel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wati/oncall-agent/internal/safety"
)

func TestGoSymbolsWithGopls(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	manager := Manager{Paths: safety.WorkspacePolicy{Roots: []string{root}}, Timeout: 20 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	symbols, err := manager.Symbols(ctx, root, "NewServer", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, sym := range symbols {
		if strings.Contains(sym.Name, "NewServer") {
			return
		}
	}
	t.Fatalf("NewServer not found in symbols: %#v", symbols)
}

func TestCSharpMissingServerMessage(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "App"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(root, "src", "App", "App.csproj"), "<Project />"); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(root, "src", "App.sln"), ""); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Paths: safety.WorkspacePolicy{Roots: []string{root}}, Timeout: time.Second}
	_, err := manager.Symbols(context.Background(), root, "Program", 10)
	if err == nil {
		t.Fatal("expected missing csharp-ls error")
	}
	if !strings.Contains(err.Error(), "csharp-ls") {
		t.Fatalf("error = %v, want csharp-ls hint", err)
	}
	if !strings.Contains(err.Error(), "src") {
		t.Fatalf("error = %v, want nested project path", err)
	}
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

package health

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/observability"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

func TestServiceReportsMissingCriticalTools(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	reg.Register(fakeTool{name: "code-search"})
	rec := observability.NewRecorder()

	service := NewService(reg, []string{root}, rec, false)
	snap := service.Probe(context.Background())

	if snap.Overall != StatusUnhealthy {
		t.Fatalf("Overall = %s, want unhealthy", snap.Overall)
	}
	if !strings.Contains(service.SummaryPrompt(), "repo-search") {
		t.Fatalf("SummaryPrompt() should mention missing repo-search, got %q", service.SummaryPrompt())
	}
}

func TestServiceReportsStaleRAGIndex(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	writeFile(t, repo, "README.md", "initial\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	oldCommit := gitOutput(t, repo, "rev-parse", "main")
	writeFile(t, repo, "README.md", "next\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "next")

	rec := observability.NewRecorder()
	rec.RAGIndexSuccess(repo, "main", oldCommit, 1, 1, 0, 0, 0, time.Millisecond)
	reg := registry.New()
	for _, name := range []string{"code-search", "code-read_file", "repo-search", "repo-read_file", "rag-search"} {
		reg.Register(fakeTool{name: name})
	}

	service := NewService(reg, []string{filepath.Dir(repo)}, rec, true)
	snap := service.Probe(context.Background())

	if snap.RAG.Status != StatusDegraded {
		t.Fatalf("RAG.Status = %s, want degraded: %#v", snap.RAG.Status, snap.RAG)
	}
	if len(snap.RAG.Indexes) != 1 || !snap.RAG.Indexes[0].Stale {
		t.Fatalf("expected stale RAG index, got %#v", snap.RAG.Indexes)
	}
}

type fakeTool struct {
	name string
}

func (t fakeTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        t.name,
			Description: "fake",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (t fakeTool) Execute(context.Context, json.RawMessage, registry.Runtime) (registry.Result, error) {
	return registry.Result{Content: "ok"}, nil
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func gitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, repo, path, content string) {
	t.Helper()
	fullPath := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

package health

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wati/oncall-agent/internal/observability"
	"github.com/wati/oncall-agent/internal/prompts"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
	StatusUnknown   Status = "unknown"
)

type Snapshot struct {
	Overall   Status      `json:"overall"`
	CheckedAt time.Time   `json:"checked_at"`
	Tools     []ToolState `json:"tools"`
	RAG       RAGState    `json:"rag"`
}

type ToolState struct {
	Name        string         `json:"name"`
	Status      Status         `json:"status"`
	Criticality string         `json:"criticality"`
	CheckedAt   time.Time      `json:"checked_at"`
	LatencyMS   int64          `json:"latency_ms,omitempty"`
	Message     string         `json:"message,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

type RAGState struct {
	Enabled bool             `json:"enabled"`
	Status  Status           `json:"status"`
	Indexes []RAGIndexHealth `json:"indexes,omitempty"`
	Message string           `json:"message,omitempty"`
}

type RAGIndexHealth struct {
	Repo           string    `json:"repo"`
	Branch         string    `json:"branch"`
	CurrentCommit  string    `json:"current_commit,omitempty"`
	IndexedCommit  string    `json:"indexed_commit,omitempty"`
	LastIndexedAt  time.Time `json:"last_indexed_at,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	Stale          bool      `json:"stale"`
	LastDurationMS int64     `json:"last_duration_ms,omitempty"`
}

type Service struct {
	Registry       *registry.Registry
	WorkspaceRoots []string
	Metrics        *observability.Recorder
	RAGEnabled     bool
	Interval       time.Duration

	mu       sync.RWMutex
	snapshot Snapshot
}

func NewService(reg *registry.Registry, roots []string, metrics *observability.Recorder, ragEnabled bool) *Service {
	return &Service{
		Registry:       reg,
		WorkspaceRoots: append([]string(nil), roots...),
		Metrics:        metrics,
		RAGEnabled:     ragEnabled,
		Interval:       time.Minute,
		snapshot: Snapshot{
			Overall:   StatusUnknown,
			CheckedAt: time.Now().UTC(),
		},
	}
}

func (s *Service) Start(ctx context.Context) {
	s.Probe(ctx)
	interval := s.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Probe(ctx)
		}
	}
}

func (s *Service) Probe(ctx context.Context) Snapshot {
	now := time.Now().UTC()
	tools := []ToolState{
		s.probeRegisteredTool(now, "code-search", "critical"),
		s.probeRegisteredTool(now, "code-read_file", "critical"),
		s.probeRegisteredTool(now, "repo-search", "critical"),
		s.probeRegisteredTool(now, "repo-read_file", "critical"),
		s.probeRegisteredTool(now, "rag-search", "critical"),
	}
	tools = append(tools, s.probeWorkspace(now), s.probeGitRepo(ctx, now), s.probeRipgrep(ctx, now))
	rag := s.probeRAG(ctx, now)
	snap := Snapshot{
		Overall:   overallStatus(tools, rag),
		CheckedAt: now,
		Tools:     tools,
		RAG:       rag,
	}
	s.mu.Lock()
	s.snapshot = snap
	s.mu.Unlock()
	return snap
}

func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *Service) SummaryPrompt() string {
	snap := s.Snapshot()
	if snap.Overall == StatusHealthy || snap.Overall == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(prompts.HealthPrompt("summary_header", ""))
	for _, tool := range snap.Tools {
		if tool.Status == StatusHealthy {
			continue
		}
		b.WriteString(fmt.Sprintf("- %s: %s", tool.Name, tool.Status))
		if tool.Message != "" {
			b.WriteString(" (" + tool.Message + ")")
		}
		b.WriteString("\n")
	}
	if snap.RAG.Status != StatusHealthy && snap.RAG.Status != "" {
		b.WriteString(fmt.Sprintf("- rag: %s", snap.RAG.Status))
		if snap.RAG.Message != "" {
			b.WriteString(" (" + snap.RAG.Message + ")")
		}
		b.WriteString("\n")
	}
	b.WriteString(prompts.HealthPrompt("summary_rules", ""))
	return strings.TrimSpace(b.String())
}

func (s *Service) RAGSnapshot() RAGState {
	return s.Snapshot().RAG
}

func (s *Service) probeRegisteredTool(now time.Time, name, criticality string) ToolState {
	start := time.Now()
	state := ToolState{Name: name, Criticality: criticality, CheckedAt: now, LatencyMS: time.Since(start).Milliseconds()}
	if s.Registry == nil || !s.Registry.Has(name) {
		state.Status = StatusUnhealthy
		state.Message = "tool is not registered"
		return state
	}
	state.Status = StatusHealthy
	state.Message = "registered"
	return state
}

func (s *Service) probeWorkspace(now time.Time) ToolState {
	start := time.Now()
	state := ToolState{Name: "workspace-roots", Criticality: "critical", CheckedAt: now}
	defer func() { state.LatencyMS = time.Since(start).Milliseconds() }()
	if len(s.WorkspaceRoots) == 0 {
		state.Status = StatusUnhealthy
		state.Message = "WORKSPACE_ROOTS is empty"
		return state
	}
	for _, root := range s.WorkspaceRoots {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			state.Status = StatusUnhealthy
			state.Message = "workspace root is not readable: " + root
			return state
		}
	}
	state.Status = StatusHealthy
	state.Message = "workspace roots readable"
	state.Details = map[string]any{"roots": s.WorkspaceRoots}
	return state
}

func (s *Service) probeGitRepo(ctx context.Context, now time.Time) ToolState {
	start := time.Now()
	state := ToolState{Name: "git-repos", Criticality: "critical", CheckedAt: now}
	defer func() { state.LatencyMS = time.Since(start).Milliseconds() }()
	repos := discoverRepos(s.WorkspaceRoots)
	if len(repos) == 0 {
		state.Status = StatusDegraded
		state.Message = "no git repos discovered under workspace roots"
		return state
	}
	repo := repos[0]
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "--no-optional-locks", "rev-parse", "--is-inside-work-tree")
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "true" {
		state.Status = StatusUnhealthy
		state.Message = "git rev-parse failed for " + repo
		return state
	}
	state.Status = StatusHealthy
	state.Message = "git repos accessible"
	state.Details = map[string]any{"repo_count": len(repos), "sample_repo": repo}
	return state
}

func (s *Service) probeRipgrep(ctx context.Context, now time.Time) ToolState {
	start := time.Now()
	state := ToolState{Name: "rg", Criticality: "important", CheckedAt: now}
	defer func() { state.LatencyMS = time.Since(start).Milliseconds() }()
	if _, err := exec.LookPath("rg"); err != nil {
		state.Status = StatusUnhealthy
		state.Message = "rg is not installed or not in PATH"
		return state
	}
	root := firstRoot(s.WorkspaceRoots)
	if root == "" {
		state.Status = StatusUnknown
		state.Message = "no workspace root to probe"
		return state
	}
	cmd := exec.CommandContext(ctx, "rg", "--files", root)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		state.Status = StatusDegraded
		state.Message = "rg --files returned no output"
		return state
	}
	state.Status = StatusHealthy
	state.Message = "rg can list workspace files"
	return state
}

func (s *Service) probeRAG(ctx context.Context, now time.Time) RAGState {
	if !s.RAGEnabled {
		return RAGState{Enabled: false, Status: StatusUnknown, Message: "RAG is disabled"}
	}
	if s.Metrics == nil {
		return RAGState{Enabled: true, Status: StatusUnknown, Message: "metrics recorder unavailable"}
	}
	metrics := s.Metrics.Snapshot()
	if len(metrics.RAG.Indexes) == 0 {
		return RAGState{Enabled: true, Status: StatusDegraded, Message: "no RAG index state observed yet"}
	}
	state := RAGState{Enabled: true, Status: StatusHealthy}
	for _, idx := range metrics.RAG.Indexes {
		current := resolveCommit(ctx, idx.Repo, idx.Branch)
		stale := current != "" && idx.LastCommit != "" && current != idx.LastCommit
		item := RAGIndexHealth{
			Repo:           idx.Repo,
			Branch:         idx.Branch,
			CurrentCommit:  shortSHA(current),
			IndexedCommit:  shortSHA(idx.LastCommit),
			LastIndexedAt:  idx.LastIndexedAt,
			LastError:      idx.LastError,
			Stale:          stale,
			LastDurationMS: idx.LastDurationMS,
		}
		state.Indexes = append(state.Indexes, item)
		if idx.LastError != "" {
			state.Status = worseStatus(state.Status, StatusUnhealthy)
			state.Message = "one or more RAG indexes have errors"
		} else if stale {
			state.Status = worseStatus(state.Status, StatusDegraded)
			state.Message = "one or more RAG indexes are stale"
		}
	}
	return state
}

func overallStatus(tools []ToolState, rag RAGState) Status {
	overall := StatusHealthy
	for _, tool := range tools {
		if tool.Criticality == "critical" {
			overall = worseStatus(overall, tool.Status)
		} else if tool.Status == StatusUnhealthy {
			overall = worseStatus(overall, StatusDegraded)
		}
	}
	if rag.Enabled {
		overall = worseStatus(overall, rag.Status)
	}
	return overall
}

func worseStatus(a, b Status) Status {
	rank := map[Status]int{
		StatusHealthy:   0,
		StatusUnknown:   1,
		StatusDegraded:  2,
		StatusUnhealthy: 3,
	}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func discoverRepos(roots []string) []string {
	var repos []string
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		if isGitRepo(root) {
			repos = append(repos, root)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(root, entry.Name())
			if isGitRepo(path) {
				repos = append(repos, path)
			}
		}
	}
	return repos
}

func isGitRepo(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && info.IsDir()
}

func firstRoot(roots []string) string {
	for _, root := range roots {
		if strings.TrimSpace(root) != "" {
			return root
		}
	}
	return ""
}

func resolveCommit(ctx context.Context, repo, branch string) string {
	if repo == "" || branch == "" {
		return ""
	}
	for _, ref := range []string{"origin/" + branch, branch} {
		cmd := exec.CommandContext(ctx, "git", "-C", repo, "--no-optional-locks", "rev-parse", ref)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Run(); err == nil {
			return strings.TrimSpace(stdout.String())
		}
	}
	return ""
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

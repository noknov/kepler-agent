package health

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
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

type Service struct {
	Registry       *registry.Registry
	WorkspaceRoots []string
	Interval       time.Duration
	Redis          *redisclient.Client

	mu       sync.RWMutex
	snapshot Snapshot
}

const (
	healthSnapshotKey = "health:snapshot"
	healthProbeLock   = "health:probe:lock"
	healthSnapshotTTL = 2 * time.Minute
	healthLockTTL     = 90 * time.Second
)

func NewService(reg *registry.Registry, roots []string) *Service {
	return &Service{
		Registry:       reg,
		WorkspaceRoots: append([]string(nil), roots...),
		Interval:       time.Minute,
		snapshot: Snapshot{
			Overall:   StatusUnknown,
			CheckedAt: time.Now().UTC(),
		},
	}
}

func (s *Service) Start(ctx context.Context) {
	s.probeOrFetchCached(ctx)
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
			s.probeOrFetchCached(ctx)
		}
	}
}

func (s *Service) probeOrFetchCached(ctx context.Context) {
	if s.Redis == nil {
		s.Probe(ctx)
		return
	}
	acquired, _ := s.Redis.SetNX(ctx, healthProbeLock, "1", healthLockTTL)
	if acquired {
		snap := s.Probe(ctx)
		if data, err := json.Marshal(snap); err == nil {
			_ = s.Redis.Set(ctx, healthSnapshotKey, string(data), healthSnapshotTTL)
		}
		return
	}
	if cached, err := s.Redis.Get(ctx, healthSnapshotKey); err == nil && cached != "" {
		var snap Snapshot
		if json.Unmarshal([]byte(cached), &snap) == nil {
			s.mu.Lock()
			s.snapshot = snap
			s.mu.Unlock()
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
	}
	tools = append(tools, s.probeWorkspace(now), s.probeGitRepo(ctx, now), s.probeRipgrep(ctx, now))
	snap := Snapshot{
		Overall:   overallStatus(tools),
		CheckedAt: now,
		Tools:     tools,
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
	b.WriteString(prompts.HealthPrompt("summary_rules", ""))
	return strings.TrimSpace(b.String())
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
	var checked []map[string]string
	var failures []string
	for _, repo := range repos {
		item := map[string]string{"repo": repo}
		cmd := exec.CommandContext(ctx, "git", "-C", repo, "--no-optional-locks", "rev-parse", "--is-inside-work-tree")
		if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "true" {
			failures = append(failures, filepath.Base(repo)+": git rev-parse failed")
			item["status"] = "rev-parse failed"
			checked = append(checked, item)
			continue
		}
		if origin := gitCommandOutput(ctx, repo, "remote", "get-url", "origin"); origin == "" {
			failures = append(failures, filepath.Base(repo)+": origin remote missing")
			item["status"] = "origin missing"
			checked = append(checked, item)
			continue
		}
		branch := localDefaultBranch(ctx, repo)
		if branch == "" {
			failures = append(failures, filepath.Base(repo)+": default branch ref not resolvable")
			item["status"] = "default branch unresolved"
			checked = append(checked, item)
			continue
		}
		item["status"] = "ok"
		item["default_branch"] = branch
		checked = append(checked, item)
	}
	if len(failures) > 0 {
		state.Status = StatusUnhealthy
		state.Message = strings.Join(failures, "; ")
		state.Details = map[string]any{"repo_count": len(repos), "repos": checked}
		return state
	}
	state.Status = StatusHealthy
	state.Message = "git repos accessible and default refs resolvable; fetch freshness is checked by git tools on request"
	state.Details = map[string]any{"repo_count": len(repos), "repos": checked}
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

func overallStatus(tools []ToolState) Status {
	overall := StatusHealthy
	for _, tool := range tools {
		if tool.Criticality == "critical" {
			overall = worseStatus(overall, tool.Status)
		} else if tool.Status == StatusUnhealthy {
			overall = worseStatus(overall, StatusDegraded)
		}
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

func localDefaultBranch(ctx context.Context, repo string) string {
	if out := gitCommandOutput(ctx, repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); out != "" {
		return strings.TrimPrefix(out, "origin/")
	}
	for _, branch := range []string{"main", "master"} {
		if resolveCommit(ctx, repo, branch) != "" {
			return branch
		}
	}
	return ""
}

func gitCommandOutput(ctx context.Context, repo string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo, "--no-optional-locks"}, args...)...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}

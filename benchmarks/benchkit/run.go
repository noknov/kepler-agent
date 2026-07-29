package benchkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type RunOptions struct {
	Timeout        time.Duration
	WorkspaceRoot  string
	KeepWorkspaces bool
	Agent          string
	MaxPatchBytes  int
	OutputPath     string
	Limit          int
}

func RunSuite(ctx context.Context, suite Suite, agent Agent, out io.Writer, opts RunOptions) (Summary, []CaseResult, error) {
	if err := suite.Validate(); err != nil {
		return Summary{}, nil, err
	}
	if agent == nil {
		return Summary{}, nil, fmt.Errorf("agent is required")
	}
	defaultTimeout := opts.Timeout
	if defaultTimeout <= 0 {
		defaultTimeout = 10 * time.Minute
	}
	workspaceRoot, cleanup, err := prepareWorkspaceRoot(suite.Name, opts.WorkspaceRoot, opts.KeepWorkspaces)
	if err != nil {
		return Summary{}, nil, err
	}
	defer cleanup()
	start := time.Now()
	results := make([]CaseResult, 0, len(suite.Cases))
	encoder := json.NewEncoder(out)
	for _, c := range suite.Cases {
		result := runCase(ctx, c, agent, defaultTimeout, workspaceRoot, opts)
		results = append(results, result)
		if out != nil {
			if err := encoder.Encode(result); err != nil {
				return Summary{}, results, err
			}
		}
	}
	summary := summarize(suite.Name, results, time.Since(start))
	return summary, results, nil
}

func runCase(ctx context.Context, c Case, agent Agent, defaultTimeout time.Duration, workspaceRoot string, opts RunOptions) CaseResult {
	start := time.Now()
	caseCtx, cancel := context.WithTimeout(ctx, c.Timeout(defaultTimeout))
	defer cancel()
	caseWorkspace, prepErr := prepareCaseWorkspace(c, workspaceRoot)
	var agentResult AgentResult
	var err error
	snapshot := ""
	if prepErr != nil {
		err = prepErr
	} else {
		c.Workspace = caseWorkspace
		if setupErr := runSetup(caseCtx, c); setupErr != nil {
			err = setupErr
		} else {
			snapshot, err = snapshotWorkspace(c, workspaceRoot)
			if err == nil {
				agentResult, err = agent.RunCase(caseCtx, c)
			}
		}
	}
	patch := ""
	if err == nil && snapshot != "" {
		patch = capturePatch(caseCtx, snapshot, caseWorkspace, opts.MaxPatchBytes)
		agentResult.Patch = patch
	}
	if snapshot != "" {
		_ = os.RemoveAll(snapshot)
	}
	checks, passed, score := Grade(caseCtx, c, agentResult)
	result := CaseResult{
		ID:                c.ID,
		Category:          c.Category,
		Title:             c.Title,
		Passed:            passed && err == nil,
		Score:             score,
		DurationMillis:    time.Since(start).Milliseconds(),
		Workspace:         caseWorkspace,
		Patch:             patch,
		Output:            agentResult.Output,
		TerminationReason: agentResult.TerminationReason,
		LLMCalls:          agentResult.LLMCalls,
		ToolCalls:         agentResult.ToolCalls,
		ToolCallCounts:    agentResult.ToolCallCounts,
		Checks:            checks,
	}
	if err != nil {
		result.Error = err.Error()
		result.Passed = false
	}
	if !opts.KeepWorkspaces && caseWorkspace != "" {
		_ = os.RemoveAll(caseWorkspace)
		result.Workspace = ""
	}
	return result
}

func runSetup(ctx context.Context, c Case) error {
	for _, command := range c.Setup {
		if _, err := runWorkspaceCommand(ctx, c.Workspace, command); err != nil {
			return fmt.Errorf("setup %q failed: %w", strings.Join(command.Argv, " "), err)
		}
	}
	return nil
}

func snapshotWorkspace(c Case, workspaceRoot string) (string, error) {
	snapshot := filepath.Join(workspaceRoot, safeName(c.ID)+".before")
	if err := os.RemoveAll(snapshot); err != nil {
		return "", err
	}
	if err := os.MkdirAll(snapshot, 0o700); err != nil {
		return "", err
	}
	if err := copyDir(c.Workspace, snapshot); err != nil {
		return "", err
	}
	return snapshot, nil
}

func capturePatch(ctx context.Context, before, after string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 200000
	}
	cmd := exec.CommandContext(ctx, "diff", "-ruN", before, after)
	out, err := cmd.CombinedOutput()
	if len(out) == 0 || err == nil {
		return ""
	}
	text := string(out)
	text = strings.ReplaceAll(text, before, "a")
	text = strings.ReplaceAll(text, after, "b")
	if len(text) > maxBytes {
		text = text[:maxBytes] + "\n...[truncated]"
	}
	return text
}

func runWorkspaceCommand(ctx context.Context, workspace string, command Command) (string, error) {
	if len(command.Argv) == 0 {
		return "", fmt.Errorf("command argv is required")
	}
	timeout := time.Duration(command.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	workdir := workspace
	if command.Workdir != "" {
		workdir = command.Workdir
		if !filepath.IsAbs(workdir) {
			workdir = filepath.Join(workspace, workdir)
		}
	}
	cmd := exec.CommandContext(cmdCtx, command.Argv[0], command.Argv[1:]...)
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("%w: %s", err, text)
	}
	return text, nil
}

func prepareWorkspaceRoot(suiteName, configured string, keep bool) (string, func(), error) {
	if configured != "" {
		if err := os.MkdirAll(configured, 0o700); err != nil {
			return "", func() {}, err
		}
		return configured, func() {}, nil
	}
	root, err := os.MkdirTemp("", safeName("bench-"+suiteName)+"-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() {
		if !keep {
			_ = os.RemoveAll(root)
		}
	}
	return root, cleanup, nil
}

func prepareCaseWorkspace(c Case, workspaceRoot string) (string, error) {
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	dst := filepath.Join(workspaceRoot, safeName(c.ID))
	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return "", err
	}
	if c.Workspace == "" {
		return dst, writeCaseFiles(dst, c.Files)
	}
	if err := copyDir(c.Workspace, dst); err != nil {
		return "", err
	}
	return dst, writeCaseFiles(dst, c.Files)
}

func writeCaseFiles(workspace string, files map[string]string) error {
	for path, content := range files {
		clean := filepath.Clean(path)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
			return fmt.Errorf("case file path %q is outside workspace", path)
		}
		target := filepath.Join(workspace, clean)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func safeName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "case"
	}
	return string(out)
}

func summarize(name string, results []CaseResult, d time.Duration) Summary {
	s := Summary{
		Suite:          name,
		Total:          len(results),
		DurationMillis: d.Milliseconds(),
		ByCategory:     map[string]Stat{},
	}
	var score float64
	for _, r := range results {
		if r.Passed {
			s.Passed++
		}
		score += r.Score
		stat := s.ByCategory[r.Category]
		stat.Total++
		if r.Passed {
			stat.Passed++
		}
		stat.Score += r.Score
		s.ByCategory[r.Category] = stat
	}
	if s.Total > 0 {
		s.Score = score / float64(s.Total)
	}
	for category, stat := range s.ByCategory {
		if stat.Total > 0 {
			stat.Score = stat.Score / float64(stat.Total)
		}
		s.ByCategory[category] = stat
	}
	return s
}

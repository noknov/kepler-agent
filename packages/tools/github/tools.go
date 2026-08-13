package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
)

const (
	defaultAPIBaseURL = "https://api.github.com"
)

type Client struct {
	Token      string
	APIBaseURL string
	Owner      string
	Repo       string
	HTTP       *http.Client
}

func (c Client) enabled() bool {
	return strings.TrimSpace(c.Token) != ""
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c Client) logHTTPClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 90 * time.Second}
}

func (c Client) baseURL() string {
	baseURL := strings.TrimRight(strings.TrimSpace(c.APIBaseURL), "/")
	if baseURL == "" {
		return defaultAPIBaseURL
	}
	return baseURL
}

func (c Client) defaultRepository() string {
	owner := strings.TrimSpace(c.Owner)
	repo := strings.TrimSpace(c.Repo)
	if owner == "" || repo == "" {
		return ""
	}
	return owner + "/" + repo
}

type DispatchWorkflowTool struct {
	Client Client
}

func (DispatchWorkflowTool) IsWrite() bool { return true }

func (t DispatchWorkflowTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"github-dispatch_workflow",
		"",
		registry.ObjectSchema([]string{"workflow", "ref"}, map[string]any{
			"repository": map[string]any{"type": "string", "description": ""},
			"workflow":   map[string]any{"type": "string", "description": ""},
			"ref":        map[string]any{"type": "string", "description": ""},
			"inputs": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
				"description":          "",
			},
		}),
	)
}

func (t DispatchWorkflowTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	if !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("GitHub is not configured: GITHUB_TOKEN is required")
	}
	var args struct {
		Repository string            `json:"repository"`
		Workflow   string            `json:"workflow"`
		Ref        string            `json:"ref"`
		Inputs     map[string]string `json:"inputs"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	repository := strings.TrimSpace(args.Repository)
	if repository == "" {
		repository = t.Client.defaultRepository()
	}
	if repository == "" {
		return registry.Result{}, fmt.Errorf("repository is required unless GITHUB_DEFAULT_OWNER and GITHUB_DEFAULT_REPO are configured")
	}
	workflow := resolveWorkflow(args.Workflow)
	if workflow == "" {
		return registry.Result{}, fmt.Errorf("workflow is required; use inbox or all-services when appropriate")
	}
	ref := strings.TrimSpace(args.Ref)
	if ref == "" {
		return registry.Result{}, fmt.Errorf("ref is required")
	}
	if err := t.Client.dispatch(ctx, repository, workflow, ref, args.Inputs); err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: fmt.Sprintf("dispatched GitHub workflow repository=%s workflow=%s ref=%s inputs=%s", repository, workflow, ref, formatInputs(args.Inputs))}, nil
}

type WorkflowRunsTool struct {
	Client Client
}

func (WorkflowRunsTool) Parallel() bool { return true }

func (t WorkflowRunsTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"github-workflow_runs",
		"",
		registry.ObjectSchema([]string{"workflow"}, map[string]any{
			"repository": map[string]any{"type": "string", "description": ""},
			"workflow":   map[string]any{"type": "string", "description": ""},
			"branch":     map[string]any{"type": "string", "description": ""},
			"limit":      map[string]any{"type": "integer", "description": ""},
		}),
	)
}

func (t WorkflowRunsTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	if !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("GitHub is not configured: GITHUB_TOKEN is required")
	}
	var args struct {
		Repository string `json:"repository"`
		Workflow   string `json:"workflow"`
		Branch     string `json:"branch"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	repository := strings.TrimSpace(args.Repository)
	if repository == "" {
		repository = t.Client.defaultRepository()
	}
	if repository == "" {
		return registry.Result{}, fmt.Errorf("repository is required unless GITHUB_DEFAULT_OWNER and GITHUB_DEFAULT_REPO are configured")
	}
	workflow := resolveWorkflow(args.Workflow)
	if workflow == "" {
		return registry.Result{}, fmt.Errorf("workflow is required; use inbox or all-services when appropriate")
	}
	if args.Limit <= 0 {
		args.Limit = 5
	}
	if args.Limit > 20 {
		args.Limit = 20
	}
	runs, err := t.Client.workflowRuns(ctx, repository, workflow, strings.TrimSpace(args.Branch), args.Limit)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: summarizeRuns(runs)}, nil
}

func (c Client) dispatch(ctx context.Context, repository, workflow, ref string, inputs map[string]string) error {
	payload := map[string]any{
		"ref": ref,
	}
	if len(inputs) > 0 {
		payload["inputs"] = inputs
	}
	endpoint, err := c.workflowEndpoint(repository, workflow, "dispatches", nil)
	if err != nil {
		return err
	}
	data, err := c.do(ctx, http.MethodPost, endpoint, payload)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		return fmt.Errorf("github dispatch returned unexpected body: %s", string(data))
	}
	return nil
}

func (c Client) workflowRuns(ctx context.Context, repository, workflow, branch string, limit int) ([]workflowRun, error) {
	values := url.Values{}
	values.Set("per_page", fmt.Sprintf("%d", limit))
	if branch != "" {
		values.Set("branch", branch)
	}
	endpoint, err := c.workflowEndpoint(repository, workflow, "runs", values)
	if err != nil {
		return nil, err
	}
	data, err := c.do(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Runs []workflowRun `json:"workflow_runs"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	return parsed.Runs, nil
}

type workflowRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Event      string `json:"event"`
	HTMLURL    string `json:"html_url"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

func (c Client) workflowEndpoint(repository, workflow, suffix string, values url.Values) (string, error) {
	owner, repo, err := splitRepository(repository)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf(
		"%s/repos/%s/%s/actions/workflows/%s/%s",
		c.baseURL(),
		url.PathEscape(owner),
		url.PathEscape(repo),
		url.PathEscape(workflow),
		suffix,
	)
	if len(values) > 0 {
		path += "?" + values.Encode()
	}
	return path, nil
}

func (c Client) do(ctx context.Context, method, endpoint string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("github status %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

func resolveWorkflow(workflow string) string {
	workflow = strings.TrimSpace(workflow)
	return prompts.GitHubWorkflow(workflow, workflow)
}

func splitRepository(repository string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(repository), "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("repository must be in owner/repo form")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func formatInputs(inputs map[string]string) string {
	if len(inputs) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+inputs[key])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func summarizeRuns(runs []workflowRun) string {
	if len(runs) == 0 {
		return "no workflow runs"
	}
	lines := make([]string, 0, len(runs))
	for _, run := range runs {
		sha := run.HeadSHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		conclusion := run.Conclusion
		if conclusion == "" {
			conclusion = "-"
		}
		lines = append(lines, fmt.Sprintf("- #%d %s branch=%s sha=%s status=%s conclusion=%s event=%s url=%s", run.ID, run.Name, run.HeadBranch, sha, run.Status, conclusion, run.Event, run.HTMLURL))
	}
	return strings.Join(lines, "\n")
}

// JobLogsTool fetches GitHub Actions job logs for a specific workflow run.
// Supports pagination via start_line and max_lines so the LLM can browse
// through large logs in multiple calls.
type JobLogsTool struct {
	Client Client
}

const (
	defaultLogPageLines = 200
	maxLogPageLines     = 500
)

func (JobLogsTool) Parallel() bool { return true }

func (t JobLogsTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"github-job_logs",
		"",
		registry.ObjectSchema(nil, map[string]any{
			"repository": map[string]any{"type": "string", "description": ""},
			"url":        map[string]any{"type": "string", "description": "Optional GitHub Actions run or job URL. When provided, repository, run_id, and job_id are extracted from it."},
			"run_id":     map[string]any{"type": "integer", "description": ""},
			"job_id":     map[string]any{"type": "integer", "description": ""},
			"start_line": map[string]any{"type": "integer", "description": ""},
			"max_lines":  map[string]any{"type": "integer", "description": ""},
		}),
	)
}

func (t JobLogsTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	if !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("GitHub is not configured: GITHUB_TOKEN is required")
	}
	var args struct {
		Repository string      `json:"repository"`
		URL        string      `json:"url"`
		RunID      json.Number `json:"run_id"`
		JobID      json.Number `json:"job_id"`
		StartLine  json.Number `json:"start_line"`
		MaxLines   json.Number `json:"max_lines"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if parsed, err := parseActionsURL(args.URL); err == nil {
		if args.Repository == "" {
			args.Repository = parsed.repository
		}
		if args.RunID == "" && parsed.runID > 0 {
			args.RunID = json.Number(fmt.Sprintf("%d", parsed.runID))
		}
		if args.JobID == "" && parsed.jobID > 0 {
			args.JobID = json.Number(fmt.Sprintf("%d", parsed.jobID))
		}
	} else if strings.TrimSpace(args.URL) != "" {
		return registry.Result{}, err
	}
	repository := strings.TrimSpace(args.Repository)
	if repository == "" {
		repository = t.Client.defaultRepository()
	}
	if repository == "" {
		return registry.Result{}, fmt.Errorf("repository is required")
	}
	runID, _ := args.RunID.Int64()
	jobID, _ := args.JobID.Int64()
	if runID <= 0 {
		return registry.Result{}, fmt.Errorf("run_id is required")
	}
	startLine, _ := args.StartLine.Int64()
	maxLines, _ := args.MaxLines.Int64()
	if maxLines <= 0 {
		maxLines = defaultLogPageLines
	}
	if maxLines > maxLogPageLines {
		maxLines = maxLogPageLines
	}

	owner, repo, err := splitRepository(repository)
	if err != nil {
		return registry.Result{}, err
	}

	// If a specific job_id is provided, fetch and paginate its log.
	if jobID > 0 {
		content, err := t.Client.fetchJobLog(ctx, owner, repo, jobID)
		if err != nil {
			return registry.Result{}, fmt.Errorf("fetch job log: %w", err)
		}
		return registry.Result{Content: paginateLog(content, int(startLine), int(maxLines))}, nil
	}

	// Otherwise list jobs for the run.
	jobs, err := t.Client.listRunJobs(ctx, owner, repo, runID)
	if err != nil {
		return registry.Result{}, fmt.Errorf("list run jobs: %w", err)
	}
	if len(jobs) == 0 {
		return registry.Result{Content: "no jobs found for this run"}, nil
	}

	var failed []runJob
	for _, j := range jobs {
		if j.Conclusion == "failure" {
			failed = append(failed, j)
		}
	}
	targets := failed
	if len(targets) == 0 {
		targets = jobs
	}
	if len(targets) > 3 {
		targets = targets[:3]
	}

	var out strings.Builder
	fmt.Fprintf(&out, "Run %d: %d jobs total, %d failed\n\n", runID, len(jobs), len(failed))

	for i, j := range jobs {
		marker := " "
		if j.Conclusion == "failure" {
			marker = "✗"
		} else if j.Conclusion == "success" {
			marker = "✓"
		}
		fmt.Fprintf(&out, "%s %s (id=%d status=%s conclusion=%s)\n", marker, j.Name, j.ID, j.Status, j.Conclusion)
		if i >= 19 {
			fmt.Fprintf(&out, "  ... and %d more\n", len(jobs)-20)
			break
		}
	}

	for _, j := range targets {
		content, err := t.Client.fetchJobLog(ctx, owner, repo, j.ID)
		if err != nil {
			fmt.Fprintf(&out, "\n--- %s (id=%d) ---\nerror fetching log: %v\n", j.Name, j.ID, err)
			continue
		}
		fmt.Fprintf(&out, "\n--- %s (id=%d) ---\n%s\n", j.Name, j.ID, paginateLog(content, int(startLine), int(maxLines)))
	}

	return registry.Result{Content: out.String()}, nil
}

// paginateLog slices a log string into a page of lines.
// If startLine is 0, defaults to showing the tail (last maxLines lines).
// If startLine is negative, counts from the end (-1 = last line).
// Returns the page content prefixed with a position header.
func paginateLog(content string, startLine, maxLines int) string {
	lines := strings.Split(content, "\n")
	total := len(lines)

	if startLine == 0 {
		// Default: show tail
		startLine = total - maxLines + 1
		if startLine < 1 {
			startLine = 1
		}
	} else if startLine < 0 {
		startLine = total + startLine + 1
		if startLine < 1 {
			startLine = 1
		}
	}

	// Clamp to valid range (1-based).
	if startLine > total {
		startLine = total
	}
	endLine := startLine + maxLines - 1
	if endLine > total {
		endLine = total
	}

	slice := lines[startLine-1 : endLine]

	var out strings.Builder
	fmt.Fprintf(&out, "[lines %d-%d of %d total]", startLine, endLine, total)
	if startLine > 1 {
		fmt.Fprintf(&out, " (use start_line=1 to see from the beginning)")
	}
	out.WriteString("\n")
	out.WriteString(strings.Join(slice, "\n"))
	return out.String()
}

type runJob struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	Steps      []jobStep `json:"steps"`
}

type jobStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Number     int    `json:"number"`
}

func (c Client) listRunJobs(ctx context.Context, owner, repo string, runID int64) ([]runJob, error) {
	var all []runJob
	for page := 1; page <= 20; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/%s/actions/runs/%d/jobs?per_page=100&page=%d",
			c.baseURL(), url.PathEscape(owner), url.PathEscape(repo), runID, page)
		data, err := c.do(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Jobs []runJob `json:"jobs"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, err
		}
		all = append(all, parsed.Jobs...)
		if len(parsed.Jobs) < 100 {
			return all, nil
		}
	}
	return all, nil
}

type actionsURLParts struct {
	repository string
	runID      int64
	jobID      int64
}

func parseActionsURL(raw string) (actionsURLParts, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return actionsURLParts{}, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return actionsURLParts{}, fmt.Errorf("url must be a GitHub Actions URL")
	}
	if !strings.EqualFold(parsed.Host, "github.com") {
		return actionsURLParts{}, fmt.Errorf("url host must be github.com")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 5 || parts[2] != "actions" || parts[3] != "runs" {
		return actionsURLParts{}, fmt.Errorf("url must look like https://github.com/owner/repo/actions/runs/<run_id>[/job/<job_id>]")
	}
	runID, err := parsePositiveInt64(parts[4], "run_id")
	if err != nil {
		return actionsURLParts{}, err
	}
	var jobID int64
	if len(parts) >= 7 && parts[5] == "job" {
		jobID, err = parsePositiveInt64(parts[6], "job_id")
		if err != nil {
			return actionsURLParts{}, err
		}
	}
	return actionsURLParts{
		repository: parts[0] + "/" + parts[1],
		runID:      runID,
		jobID:      jobID,
	}, nil
}

func parsePositiveInt64(value, name string) (int64, error) {
	n, err := json.Number(value).Int64()
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s in GitHub URL must be a positive integer", name)
	}
	return n, nil
}

func (c Client) fetchJobLog(ctx context.Context, owner, repo string, jobID int64) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/actions/jobs/%d/logs",
		c.baseURL(), url.PathEscape(owner), url.PathEscape(repo), jobID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := c.logHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2MB max
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("github status %d: %s", resp.StatusCode, truncate(string(data), 500))
	}

	return string(data), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// PRDiffTool fetches a pull request's metadata and diff from GitHub API.
type PRDiffTool struct {
	Client Client
}

func (PRDiffTool) Parallel() bool { return true }

func (t PRDiffTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"github-pr_diff",
		"Read a compact pull-request review manifest. Use github-pr_file_diff for a targeted file diff instead of paging a whole PR diff.",
		registry.ObjectSchema(nil, map[string]any{
			"repository": map[string]any{"type": "string", "description": "GitHub repository in owner/repo form. Optional when url is provided or defaults are configured."},
			"url":        map[string]any{"type": "string", "description": "GitHub pull request URL. Prefer this when the user pasted a PR URL; repository and pr are extracted from it."},
			"pr":         map[string]any{"type": "integer", "description": "Pull request number. Optional when url is provided."},
		}),
	)
}

func (t PRDiffTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	if !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("GitHub is not configured: GITHUB_TOKEN is required")
	}
	var args struct {
		Repository string      `json:"repository"`
		URL        string      `json:"url"`
		PR         json.Number `json:"pr"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if parsed, err := parsePullURL(args.URL); err == nil {
		if args.Repository == "" {
			args.Repository = parsed.repository
		}
		if args.PR == "" && parsed.number > 0 {
			args.PR = json.Number(fmt.Sprintf("%d", parsed.number))
		}
	} else if strings.TrimSpace(args.URL) != "" {
		return registry.Result{}, err
	}
	repository := strings.TrimSpace(args.Repository)
	if repository == "" {
		repository = t.Client.defaultRepository()
	}
	if repository == "" {
		return registry.Result{}, fmt.Errorf("repository is required")
	}
	prNumber, _ := args.PR.Int64()
	if prNumber <= 0 {
		return registry.Result{}, fmt.Errorf("pr number is required")
	}

	owner, repo, err := splitRepository(repository)
	if err != nil {
		return registry.Result{}, err
	}

	// Fetch PR metadata
	metaURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", t.Client.baseURL(), owner, repo, prNumber)
	metaData, err := t.Client.do(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return registry.Result{}, fmt.Errorf("fetch PR metadata for %s#%d: %w", repository, prNumber, err)
	}
	var prMeta struct {
		Title   string `json:"title"`
		Body    string `json:"body"`
		State   string `json:"state"`
		Merged  bool   `json:"merged"`
		Commits int    `json:"commits"`
		Head    struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := json.Unmarshal(metaData, &prMeta); err != nil {
		return registry.Result{}, fmt.Errorf("parse PR metadata: %w", err)
	}

	// Fetch diff
	diffURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", t.Client.baseURL(), owner, repo, prNumber)
	diffReq, err := http.NewRequestWithContext(ctx, http.MethodGet, diffURL, nil)
	if err != nil {
		return registry.Result{}, err
	}
	diffReq.Header.Set("Authorization", "Bearer "+t.Client.Token)
	diffReq.Header.Set("Accept", "application/vnd.github.diff")
	diffReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	diffResp, err := t.Client.httpClient().Do(diffReq)
	if err != nil {
		return registry.Result{}, fmt.Errorf("fetch PR diff: %w", err)
	}
	defer diffResp.Body.Close()
	diffBody, err := io.ReadAll(io.LimitReader(diffResp.Body, 2<<20)) // 2MB max from API
	if err != nil {
		return registry.Result{}, fmt.Errorf("read PR diff: %w", err)
	}
	if diffResp.StatusCode >= 300 {
		return registry.Result{}, fmt.Errorf("github diff status %d for %s#%d: %s", diffResp.StatusCode, repository, prNumber, string(diffBody))
	}

	diff := string(diffBody)

	files := diffFileIndex(diff)
	setPRDiffContext(rt, prDiffContext{
		Repository: repository,
		Number:     int(prNumber),
		HeadRef:    prMeta.Head.Ref,
		HeadSHA:    prMeta.Head.SHA,
		BaseRef:    prMeta.Base.Ref,
		ChangedFiles: func() []string {
			paths := make([]string, 0, len(files))
			for _, file := range files {
				paths = append(paths, file.Path)
			}
			return paths
		}(),
		Diff: diff,
	})

	// Return a compact, stable review manifest. The full diff remains available
	// through github-pr_file_diff so it never forces the model to paginate a
	// giant spill blob before it can inspect code at the PR head.
	var out strings.Builder
	fmt.Fprintf(&out, "PR diff context established\nrepository=%s\npr=%d\nhead_ref=%s\nhead_sha=%s\nbase_ref=%s\n", repository, prNumber, prMeta.Head.Ref, prMeta.Head.SHA, prMeta.Base.Ref)
	fmt.Fprintf(&out, "\nPR #%d: %s\n", prNumber, prMeta.Title)
	fmt.Fprintf(&out, "State: %s | Merged: %t | Commits: %d\n", prMeta.State, prMeta.Merged, prMeta.Commits)
	fmt.Fprintf(&out, "Base: %s ← Head: %s (%s)\n", prMeta.Base.Ref, prMeta.Head.Ref, prMeta.Head.SHA[:min(7, len(prMeta.Head.SHA))])
	if body := strings.TrimSpace(prMeta.Body); body != "" {
		if len(body) > 2000 {
			body = body[:2000] + "..."
		}
		fmt.Fprintf(&out, "\nDescription:\n%s\n", body)
	}
	fmt.Fprintf(&out, "\nChanged files (%d):\n", len(files))
	for _, file := range files {
		fmt.Fprintf(&out, "- %s (hunks=%d +%d/-%d)\n", file.Path, file.Hunks, file.Additions, file.Deletions)
	}
	fmt.Fprint(&out, "\nUse github-pr_file_diff with one changed path for a focused patch and PR-head line-numbered source context. Do not cite local main/default-branch line numbers for PR-head code.\n")

	return registry.Result{Content: out.String()}, nil
}

type pullURLParts struct {
	repository string
	number     int64
}

func parsePullURL(raw string) (pullURLParts, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return pullURLParts{}, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return pullURLParts{}, fmt.Errorf("url must be a GitHub pull request URL")
	}
	if !strings.EqualFold(parsed.Host, "github.com") {
		return pullURLParts{}, fmt.Errorf("url host must be github.com")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return pullURLParts{}, fmt.Errorf("url must look like https://github.com/owner/repo/pull/<pr_number>")
	}
	n, err := parsePositiveInt64(parts[3], "pr")
	if err != nil {
		return pullURLParts{}, err
	}
	return pullURLParts{repository: parts[0] + "/" + parts[1], number: n}, nil
}

type prDiffContext struct {
	Repository   string
	Number       int
	HeadRef      string
	HeadSHA      string
	BaseRef      string
	ChangedFiles []string
	Diff         string
}

const prDiffContextCacheKey = "github-pr-diff-context"

func setPRDiffContext(rt registry.Runtime, ctx prDiffContext) {
	if rt.Cache == nil {
		return
	}
	ctx.Repository = strings.TrimSpace(ctx.Repository)
	ctx.HeadRef = strings.TrimSpace(ctx.HeadRef)
	ctx.HeadSHA = strings.TrimSpace(ctx.HeadSHA)
	ctx.BaseRef = strings.TrimSpace(ctx.BaseRef)
	rt.Cache.Set(prDiffContextCacheKey, ctx)
}

func prDiffContextFromRuntime(rt registry.Runtime) (prDiffContext, bool) {
	if rt.Cache == nil {
		return prDiffContext{}, false
	}
	v, ok := rt.Cache.Get(prDiffContextCacheKey)
	if !ok {
		return prDiffContext{}, false
	}
	ctx, ok := v.(prDiffContext)
	return ctx, ok && ctx.Repository != "" && ctx.HeadSHA != ""
}

func (p prDiffContext) containsPath(path string) bool {
	path = strings.Trim(strings.TrimSpace(path), "/")
	for _, changed := range p.ChangedFiles {
		if path == strings.Trim(changed, "/") {
			return true
		}
	}
	return false
}

type diffFile struct {
	Path                        string
	Hunks, Additions, Deletions int
}

func diffFileIndex(diff string) []diffFile {
	var files []diffFile
	current := -1
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git a/") {
			parts := strings.SplitN(strings.TrimPrefix(line, "diff --git a/"), " b/", 2)
			if len(parts) == 2 {
				files = append(files, diffFile{Path: parts[1]})
				current = len(files) - 1
			}
			continue
		}
		if current < 0 || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			files[current].Hunks++
		} else if strings.HasPrefix(line, "+") {
			files[current].Additions++
		} else if strings.HasPrefix(line, "-") {
			files[current].Deletions++
		}
	}
	return files
}

type PRFileDiffTool struct {
	Client Client
}

func (PRFileDiffTool) Parallel() bool { return true }

func (PRFileDiffTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec("github-pr_file_diff", "Read the diff for one changed file in the PR established by github-pr_diff, including line-numbered source context from the PR head when available. Use these PR-head line numbers for review citations instead of reading the local default branch.", registry.ObjectSchema([]string{"path"}, map[string]any{
		"path": map[string]any{"type": "string", "description": "Repository-relative path from the changed-file manifest."},
	}))
}

func (t PRFileDiffTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	pr, ok := prDiffContextFromRuntime(rt)
	if !ok {
		return registry.Result{}, fmt.Errorf("no PR review context; call github-pr_diff first")
	}
	path := strings.Trim(strings.TrimSpace(args.Path), "/")
	if !pr.containsPath(path) {
		var out strings.Builder
		fmt.Fprintf(&out, "%q is not a changed file in %s#%d. Choose one of the changed files below and call github-pr_file_diff again:\n", path, pr.Repository, pr.Number)
		for _, changed := range pr.ChangedFiles {
			fmt.Fprintf(&out, "- %s\n", changed)
		}
		return registry.Result{Content: strings.TrimSpace(out.String())}, nil
	}
	needle := "diff --git a/" + path + " b/" + path
	start := strings.Index(pr.Diff, needle)
	if start < 0 {
		return registry.Result{}, fmt.Errorf("diff for %q is unavailable", path)
	}
	rest := pr.Diff[start+len(needle):]
	if next := strings.Index(rest, "\ndiff --git a/"); next >= 0 {
		rest = rest[:next]
	}
	var out strings.Builder
	fileDiff := needle + rest
	fmt.Fprintf(&out, "PR %s#%d head=%s file=%s\n", pr.Repository, pr.Number, pr.HeadSHA, path)
	out.WriteString(fileDiff)
	if source, err := t.prHeadFileSource(ctx, pr, path); err == nil && strings.TrimSpace(source) != "" {
		if contextText := lineNumberedHunkContext(source, fileDiff, 3, 260); strings.TrimSpace(contextText) != "" {
			fmt.Fprintf(&out, "\n\nPR-head source context for %s at %s. Cite these line numbers for PR-head code:\n%s", path, pr.HeadSHA, contextText)
		}
	}
	return registry.Result{Content: out.String()}, nil
}

func (t PRFileDiffTool) prHeadFileSource(ctx context.Context, pr prDiffContext, path string) (string, error) {
	if !t.Client.enabled() {
		return "", fmt.Errorf("GitHub is not configured")
	}
	owner, repo, err := splitRepository(pr.Repository)
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s",
		t.Client.baseURL(),
		url.PathEscape(owner),
		url.PathEscape(repo),
		escapePathSegments(path),
		url.QueryEscape(pr.HeadSHA),
	)
	data, err := t.Client.do(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	if parsed.Type != "" && parsed.Type != "file" {
		return "", fmt.Errorf("path is not a file")
	}
	if parsed.Encoding != "base64" {
		return "", fmt.Errorf("unsupported content encoding %q", parsed.Encoding)
	}
	raw := strings.ReplaceAll(parsed.Content, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func escapePathSegments(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

var unifiedHunkHeader = regexp.MustCompile(`@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

type lineRange struct {
	start int
	end   int
}

func lineNumberedHunkContext(source, diff string, padding, maxLines int) string {
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	ranges := sourceRangesForDiff(diff, len(lines), padding)
	if len(ranges) == 0 {
		if len(lines) == 0 {
			return ""
		}
		ranges = []lineRange{{start: 1, end: min(len(lines), maxLines)}}
	}
	ranges = mergeLineRanges(ranges)
	var out strings.Builder
	written := 0
	for i, r := range ranges {
		if written >= maxLines {
			break
		}
		if i > 0 {
			out.WriteString("...\n")
		}
		for lineNo := r.start; lineNo <= r.end && lineNo <= len(lines) && written < maxLines; lineNo++ {
			fmt.Fprintf(&out, "%5d | %s\n", lineNo, lines[lineNo-1])
			written++
		}
	}
	if written >= maxLines {
		out.WriteString("...[truncated]\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

func sourceRangesForDiff(diff string, sourceLineCount, padding int) []lineRange {
	var ranges []lineRange
	for _, line := range strings.Split(diff, "\n") {
		m := unifiedHunkHeader.FindStringSubmatch(line)
		if len(m) == 0 {
			continue
		}
		start, _ := strconv.Atoi(m[1])
		count := 1
		if len(m) > 2 && m[2] != "" {
			count, _ = strconv.Atoi(m[2])
		}
		if count < 1 {
			count = 1
		}
		end := start + count - 1
		if sourceLineCount > 0 {
			start = max(1, start-padding)
			end = min(sourceLineCount, end+padding)
		}
		ranges = append(ranges, lineRange{start: start, end: end})
	}
	return ranges
}

func mergeLineRanges(ranges []lineRange) []lineRange {
	if len(ranges) == 0 {
		return nil
	}
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].start < ranges[j].start
	})
	merged := []lineRange{ranges[0]}
	for _, r := range ranges[1:] {
		last := &merged[len(merged)-1]
		if r.start <= last.end+1 {
			if r.end > last.end {
				last.end = r.end
			}
			continue
		}
		merged = append(merged, r)
	}
	return merged
}

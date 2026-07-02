package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/prompts"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
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
			"inputs":     map[string]any{"type": "object", "description": ""},
		}),
	)
}

func (t DispatchWorkflowTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	if !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("GitHub is not configured: GITHUB_TOKEN is required")
	}
	var args struct {
		Repository string         `json:"repository"`
		Workflow   string         `json:"workflow"`
		Ref        string         `json:"ref"`
		Inputs     map[string]any `json:"inputs"`
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
	inputs := normalizeInputs(args.Inputs)
	if err := t.Client.dispatch(ctx, repository, workflow, ref, inputs); err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: fmt.Sprintf("dispatched GitHub workflow repository=%s workflow=%s ref=%s inputs=%s", repository, workflow, ref, formatInputs(inputs))}, nil
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

func normalizeInputs(inputs map[string]any) map[string]string {
	if len(inputs) == 0 {
		return nil
	}
	out := make(map[string]string, len(inputs))
	for key, value := range inputs {
		key = strings.TrimSpace(key)
		if key == "" || value == nil {
			continue
		}
		switch v := value.(type) {
		case string:
			out[key] = v
		case bool:
			out[key] = fmt.Sprintf("%t", v)
		case float64:
			out[key] = strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", v), "0"), ".")
		default:
			out[key] = fmt.Sprintf("%v", v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
		return 0, fmt.Errorf("%s in GitHub Actions URL must be a positive integer", name)
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
		"",
		registry.ObjectSchema([]string{"pr"}, map[string]any{
			"repository": map[string]any{"type": "string", "description": ""},
			"pr":         map[string]any{"type": "integer", "description": ""},
		}),
	)
}

func (t PRDiffTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	if !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("GitHub is not configured: GITHUB_TOKEN is required")
	}
	var args struct {
		Repository string `json:"repository"`
		PR         int    `json:"pr"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	repository := strings.TrimSpace(args.Repository)
	if repository == "" {
		repository = t.Client.defaultRepository()
	}
	if repository == "" {
		return registry.Result{}, fmt.Errorf("repository is required")
	}
	if args.PR <= 0 {
		return registry.Result{}, fmt.Errorf("pr number is required")
	}

	owner, repo, err := splitRepository(repository)
	if err != nil {
		return registry.Result{}, err
	}

	// Fetch PR metadata
	metaURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", t.Client.baseURL(), owner, repo, args.PR)
	metaData, err := t.Client.do(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return registry.Result{}, fmt.Errorf("fetch PR metadata: %w", err)
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
	diffURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", t.Client.baseURL(), owner, repo, args.PR)
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
		return registry.Result{}, fmt.Errorf("github diff status %d: %s", diffResp.StatusCode, string(diffBody))
	}

	diff := string(diffBody)

	// Build output
	var out strings.Builder
	fmt.Fprintf(&out, "PR #%d: %s\n", args.PR, prMeta.Title)
	fmt.Fprintf(&out, "State: %s | Merged: %t | Commits: %d\n", prMeta.State, prMeta.Merged, prMeta.Commits)
	fmt.Fprintf(&out, "Base: %s ← Head: %s (%s)\n", prMeta.Base.Ref, prMeta.Head.Ref, prMeta.Head.SHA[:min(7, len(prMeta.Head.SHA))])
	if body := strings.TrimSpace(prMeta.Body); body != "" {
		if len(body) > 2000 {
			body = body[:2000] + "..."
		}
		fmt.Fprintf(&out, "\nDescription:\n%s\n", body)
	}
	fmt.Fprintf(&out, "\nDiff:\n%s", diff)

	return registry.Result{Content: out.String()}, nil
}

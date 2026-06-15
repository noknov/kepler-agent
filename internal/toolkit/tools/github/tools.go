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
	diffBody, err := io.ReadAll(io.LimitReader(diffResp.Body, 512<<10)) // 512KB max
	if err != nil {
		return registry.Result{}, fmt.Errorf("read PR diff: %w", err)
	}
	if diffResp.StatusCode >= 300 {
		return registry.Result{}, fmt.Errorf("github diff status %d: %s", diffResp.StatusCode, string(diffBody))
	}

	diff := string(diffBody)
	if len(diff) > 100000 {
		diff = diff[:100000] + "\n\n... [diff truncated, showing first 100KB] ..."
	}

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

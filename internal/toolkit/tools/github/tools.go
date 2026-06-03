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
		"Trigger a GitHub Actions workflow_dispatch run. Use only when the user explicitly asks to run CI/CD. Choose the workflow, ref, and inputs from the user's request or ask for missing required inputs.",
		registry.ObjectSchema([]string{"workflow", "ref"}, map[string]any{
			"repository": map[string]any{"type": "string", "description": "GitHub repository in owner/repo form. Defaults to configured repository when set."},
			"workflow":   map[string]any{"type": "string", "description": "Workflow file name/id or locally configured alias."},
			"ref":        map[string]any{"type": "string", "description": "Git ref in the workflow repository, usually main."},
			"inputs":     map[string]any{"type": "object", "description": "workflow_dispatch inputs. Pass service, environment, branch, image tag, or other workflow-specific inputs exactly as requested by the user."},
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
	payload := map[string]any{"repository": repository, "workflow": workflow, "ref": ref, "inputs": inputs}
	if registry.ToolNeedsConfirmation(rt, "github-dispatch_workflow") {
		key, confirmed := registry.ConfirmationState(rt, "github-dispatch_workflow", payload)
		if !confirmed {
			return registry.Result{
				Content:          fmt.Sprintf("This will trigger GitHub Actions for `%s` workflow `%s` on ref `%s` with inputs `%s`. Reply `confirm` in this thread to run this exact workflow.", repository, workflow, ref, formatInputs(inputs)),
				WaitForUser:      true,
				PendingActionKey: key,
			}, nil
		}
	}
	if err := t.Client.dispatch(ctx, repository, workflow, ref, inputs); err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: fmt.Sprintf("dispatched GitHub workflow repository=%s workflow=%s ref=%s inputs=%s", repository, workflow, ref, formatInputs(inputs))}, nil
}

type WorkflowRunsTool struct {
	Client Client
}

func (t WorkflowRunsTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"github-workflow_runs",
		"List recent GitHub Actions workflow runs. Use after triggering CI/CD or when the user asks about CI/CD status.",
		registry.ObjectSchema([]string{"workflow"}, map[string]any{
			"repository": map[string]any{"type": "string", "description": "GitHub repository in owner/repo form. Defaults to configured repository when set."},
			"workflow":   map[string]any{"type": "string", "description": "Workflow file name/id or locally configured alias."},
			"branch":     map[string]any{"type": "string", "description": "Optional branch filter."},
			"limit":      map[string]any{"type": "integer", "description": "Maximum runs to return. Defaults to 5, max 20."},
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

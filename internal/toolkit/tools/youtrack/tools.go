package youtrack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func (c Client) enabled() bool {
	return c.BaseURL != "" && c.Token != ""
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

type GetIssueTool struct{ Client Client }

func (t GetIssueTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"youtrack.get_issue",
		"Fetch a YouTrack issue by ID, including summary, description, state, assignee and recent comments when available.",
		registry.ObjectSchema([]string{"issue_id"}, map[string]any{
			"issue_id": map[string]any{"type": "string", "description": "Issue ID, e.g. WATI-123."},
		}),
	)
}

func (t GetIssueTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	if !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("YouTrack is not configured")
	}
	var args struct {
		IssueID string `json:"issue_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	fields := "idReadable,summary,description,customFields(name,value(name,localizedName,fullName,text)),comments(text,author(fullName),created)"
	endpoint := t.Client.BaseURL + "/api/issues/" + url.PathEscape(args.IssueID) + "?fields=" + url.QueryEscape(fields)
	data, err := t.Client.do(ctx, endpoint)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: string(data)}, nil
}

type SearchTool struct{ Client Client }

func (t SearchTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"youtrack.search",
		"Search YouTrack issues using YouTrack query syntax.",
		registry.ObjectSchema([]string{"query"}, map[string]any{
			"query": map[string]any{"type": "string", "description": "YouTrack query, e.g. 'State: Open text: payment'."},
			"limit": map[string]any{"type": "integer", "description": "Maximum results. Defaults to 10, max 50."},
		}),
	)
}

func (t SearchTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	if !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("YouTrack is not configured")
	}
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if args.Limit <= 0 {
		args.Limit = 10
	}
	if args.Limit > 50 {
		args.Limit = 50
	}
	fields := "idReadable,summary,resolved,customFields(name,value(name,localizedName,fullName,text))"
	values := url.Values{}
	values.Set("query", args.Query)
	values.Set("$top", fmt.Sprintf("%d", args.Limit))
	values.Set("fields", fields)
	endpoint := t.Client.BaseURL + "/api/issues?" + values.Encode()
	data, err := t.Client.do(ctx, endpoint)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: summarizeIssues(data)}, nil
}

func (c Client) do(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("youtrack status %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func summarizeIssues(data []byte) string {
	var issues []struct {
		IDReadable string `json:"idReadable"`
		Summary    string `json:"summary"`
	}
	if err := json.Unmarshal(data, &issues); err != nil {
		return string(data)
	}
	if len(issues) == 0 {
		return "no issues"
	}
	lines := make([]string, 0, len(issues))
	for _, issue := range issues {
		lines = append(lines, "- "+issue.IDReadable+" "+issue.Summary)
	}
	return strings.Join(lines, "\n")
}

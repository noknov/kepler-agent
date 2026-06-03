package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type Client struct {
	Token         string
	DatabaseID    string
	TitleProperty string
	Version       string
	HTTP          *http.Client
}

func (c Client) enabled() bool {
	return c.Token != ""
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

type SearchTool struct{ Client Client }

func (t SearchTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"notion-search",
		"Search Notion pages/databases. Use for incident notes, runbooks, and knowledge base lookup.",
		registry.ObjectSchema([]string{"query"}, map[string]any{
			"query": map[string]any{"type": "string", "description": "Search query."},
			"limit": map[string]any{"type": "integer", "description": "Maximum results. Defaults to 10, max 50."},
		}),
	)
}

func (t SearchTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	if !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("Notion is not configured")
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
	body := map[string]any{
		"query":     args.Query,
		"page_size": args.Limit,
	}
	data, err := t.Client.do(ctx, http.MethodPost, "https://api.notion.com/v1/search", body)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: summarizeNotionSearch(data)}, nil
}

type CreatePageTool struct{ Client Client }

func (t CreatePageTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"notion-create_page",
		"Create a Notion page in the configured incident/runbook database. Use only when the user asks to write a report or preserve an incident summary.",
		registry.ObjectSchema([]string{"title", "body"}, map[string]any{
			"title": map[string]any{"type": "string", "description": "Page title."},
			"body":  map[string]any{"type": "string", "description": "Markdown-ish plain text body. It will be stored as one paragraph block."},
		}),
	)
}

func (t CreatePageTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	if !t.Client.enabled() || t.Client.DatabaseID == "" {
		return registry.Result{}, fmt.Errorf("Notion database is not configured")
	}
	var args struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	body := map[string]any{
		"parent": map[string]any{"database_id": t.Client.DatabaseID},
		"properties": map[string]any{
			defaultString(t.Client.TitleProperty, "Name"): map[string]any{
				"title": []map[string]any{{"text": map[string]string{"content": args.Title}}},
			},
		},
		"children": []map[string]any{{
			"object": "block",
			"type":   "paragraph",
			"paragraph": map[string]any{
				"rich_text": []map[string]any{{"text": map[string]string{"content": truncate(args.Body, 1800)}}},
			},
		}},
	}
	data, err := t.Client.do(ctx, http.MethodPost, "https://api.notion.com/v1/pages", body)
	if err != nil {
		return registry.Result{}, err
	}
	var parsed struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	_ = json.Unmarshal(data, &parsed)
	return registry.Result{Content: fmt.Sprintf("created notion page id=%s url=%s", parsed.ID, parsed.URL)}, nil
}

func (c Client) do(ctx context.Context, method, endpoint string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Notion-Version", defaultString(c.Version, "2022-06-28"))
	req.Header.Set("Content-Type", "application/json")
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
		return nil, fmt.Errorf("notion status %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func summarizeNotionSearch(data []byte) string {
	var parsed struct {
		Results []struct {
			ID         string `json:"id"`
			Object     string `json:"object"`
			URL        string `json:"url"`
			Properties map[string]struct {
				Title []struct {
					PlainText string `json:"plain_text"`
				} `json:"title"`
			} `json:"properties"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return string(data)
	}
	var lines []string
	for _, r := range parsed.Results {
		title := ""
		for _, p := range r.Properties {
			if len(p.Title) > 0 {
				title = p.Title[0].PlainText
				break
			}
		}
		lines = append(lines, fmt.Sprintf("- %s %s %s %s", r.Object, r.ID, title, r.URL))
	}
	if len(lines) == 0 {
		return "no results"
	}
	return strings.Join(lines, "\n")
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...[truncated]"
}

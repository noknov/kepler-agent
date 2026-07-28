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

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

// notionBlock represents a minimal Notion block used for text extraction.
type notionBlock struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// Heading levels share the same rich_text structure.
	Heading1         *richTextBlock `json:"heading_1"`
	Heading2         *richTextBlock `json:"heading_2"`
	Heading3         *richTextBlock `json:"heading_3"`
	Paragraph        *richTextBlock `json:"paragraph"`
	BulletedListItem *richTextBlock `json:"bulleted_list_item"`
	NumberedListItem *richTextBlock `json:"numbered_list_item"`
	ToDo             *richTextBlock `json:"to_do"`
	Toggle           *richTextBlock `json:"toggle"`
	Quote            *richTextBlock `json:"quote"`
	Callout          *richTextBlock `json:"callout"`
	Code             *richTextBlock `json:"code"`
	HasChildren      bool           `json:"has_children"`
}

type richTextBlock struct {
	RichText []struct {
		PlainText string `json:"plain_text"`
	} `json:"rich_text"`
}

func (b notionBlock) text() string {
	for _, rt := range []*richTextBlock{
		b.Paragraph, b.Heading1, b.Heading2, b.Heading3,
		b.BulletedListItem, b.NumberedListItem, b.ToDo,
		b.Toggle, b.Quote, b.Callout, b.Code,
	} {
		if rt == nil {
			continue
		}
		var parts []string
		for _, r := range rt.RichText {
			if r.PlainText != "" {
				parts = append(parts, r.PlainText)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "")
		}
	}
	return ""
}

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
		"",
		registry.ObjectSchema([]string{"query"}, map[string]any{
			"query": map[string]any{"type": "string", "description": ""},
			"limit": map[string]any{"type": "integer", "description": ""},
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

func (c Client) do(ctx context.Context, method, endpoint string, payload any) ([]byte, error) {
	var reqBody io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Notion-Version", defaultString(c.Version, "2022-06-28"))
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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

// GetPageTool reads the full text content of a Notion page.
type GetPageTool struct{ Client Client }

func (t GetPageTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"notion-get_page",
		"",
		registry.ObjectSchema([]string{"page_id"}, map[string]any{
			"page_id": map[string]any{"type": "string", "description": ""},
			"depth":   map[string]any{"type": "integer", "description": ""},
		}),
	)
}

func (t GetPageTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	if !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("Notion is not configured")
	}
	var args struct {
		PageID string `json:"page_id"`
		Depth  int    `json:"depth"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if args.Depth <= 0 {
		args.Depth = 2
	}
	if args.Depth > 4 {
		args.Depth = 4
	}
	pageID := parseNotionID(args.PageID)
	content, err := t.Client.fetchPageContent(ctx, pageID, args.Depth, 0)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: content}, nil
}

// fetchPageContent recursively reads blocks and extracts plain text.
func (c Client) fetchPageContent(ctx context.Context, blockID string, maxDepth, depth int) (string, error) {
	type blocksResp struct {
		Results    []notionBlock `json:"results"`
		HasMore    bool          `json:"has_more"`
		NextCursor string        `json:"next_cursor"`
	}

	var lines []string
	cursor := ""
	for {
		url := fmt.Sprintf("https://api.notion.com/v1/blocks/%s/children?page_size=100", blockID)
		if cursor != "" {
			url += "&start_cursor=" + cursor
		}
		data, err := c.do(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		var resp blocksResp
		if err := json.Unmarshal(data, &resp); err != nil {
			return "", err
		}
		for _, block := range resp.Results {
			prefix := blockPrefix(block.Type)
			text := block.text()
			if text != "" {
				lines = append(lines, prefix+text)
			}
			if block.HasChildren && depth+1 < maxDepth {
				child, err := c.fetchPageContent(ctx, block.ID, maxDepth, depth+1)
				if err == nil && child != "" {
					for _, cl := range strings.Split(child, "\n") {
						lines = append(lines, "  "+cl)
					}
				}
			}
		}
		if !resp.HasMore {
			break
		}
		cursor = resp.NextCursor
	}
	return strings.Join(lines, "\n"), nil
}

func blockPrefix(blockType string) string {
	switch blockType {
	case "heading_1":
		return "# "
	case "heading_2":
		return "## "
	case "heading_3":
		return "### "
	case "bulleted_list_item":
		return "- "
	case "numbered_list_item":
		return "1. "
	case "to_do":
		return "[ ] "
	case "quote":
		return "> "
	case "code":
		return "```\n"
	default:
		return ""
	}
}

// QueryDatabaseTool queries a Notion database with optional filters and sorts.
type QueryDatabaseTool struct{ Client Client }

func (t QueryDatabaseTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"notion-query_database",
		"",
		registry.ObjectSchema([]string{"database_id"}, map[string]any{
			"database_id": map[string]any{"type": "string", "description": ""},
			"filter":      map[string]any{"type": "object", "description": ""},
			"sorts":       map[string]any{"type": "array", "description": ""},
			"limit":       map[string]any{"type": "integer", "description": ""},
		}),
	)
}

func (t QueryDatabaseTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	if !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("Notion is not configured")
	}
	var args struct {
		DatabaseID string          `json:"database_id"`
		Filter     json.RawMessage `json:"filter"`
		Sorts      json.RawMessage `json:"sorts"`
		Limit      int             `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if args.Limit <= 0 {
		args.Limit = 20
	}
	if args.Limit > 100 {
		args.Limit = 100
	}
	dbID := parseNotionID(args.DatabaseID)
	if dbID == "" {
		dbID = parseNotionID(t.Client.DatabaseID)
	}
	if dbID == "" {
		return registry.Result{}, fmt.Errorf("database_id is required")
	}

	body := map[string]any{"page_size": args.Limit}
	if len(args.Filter) > 0 && string(args.Filter) != "null" {
		var f any
		if err := json.Unmarshal(args.Filter, &f); err == nil {
			body["filter"] = f
		}
	}
	if len(args.Sorts) > 0 && string(args.Sorts) != "null" {
		var s any
		if err := json.Unmarshal(args.Sorts, &s); err == nil {
			body["sorts"] = s
		}
	}

	url := fmt.Sprintf("https://api.notion.com/v1/databases/%s/query", dbID)
	data, err := t.Client.do(ctx, http.MethodPost, url, body)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: summarizeNotionSearch(data)}, nil
}

// parseNotionID extracts a Notion UUID from a raw ID or a Notion page URL.
// Notion URLs look like: https://www.notion.so/Title-<32-hex-chars>
// or https://www.notion.so/<workspace>/<32-hex-chars>
func parseNotionID(input string) string {
	input = strings.TrimSpace(input)
	// Strip URL prefix if present.
	if idx := strings.LastIndex(input, "/"); idx >= 0 {
		input = input[idx+1:]
	}
	// Strip query string.
	if idx := strings.Index(input, "?"); idx >= 0 {
		input = input[:idx]
	}
	// Strip fragment.
	if idx := strings.Index(input, "#"); idx >= 0 {
		input = input[:idx]
	}
	// Some URLs encode the ID as the last 32 hex chars after a dash.
	if idx := strings.LastIndex(input, "-"); idx >= 0 && len(input)-idx-1 == 32 {
		input = input[idx+1:]
	}
	// Normalize: remove dashes and re-insert in UUID format if it looks like a 32-char hex string.
	bare := strings.ReplaceAll(input, "-", "")
	if len(bare) == 32 {
		return fmt.Sprintf("%s-%s-%s-%s-%s", bare[0:8], bare[8:12], bare[12:16], bare[16:20], bare[20:32])
	}
	return input
}

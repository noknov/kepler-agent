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

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
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

func (t SearchTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"notion-search",
		"",
		tool.ObjectSchema([]string{"query"}, map[string]any{
			"query": map[string]any{"type": "string", "description": ""},
			"limit": map[string]any{"type": "integer", "description": ""},
		}),
	)
}

func (t SearchTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	if !t.Client.enabled() {
		return tool.Result{}, fmt.Errorf("Notion is not configured")
	}
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
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
		return tool.Result{}, err
	}
	content, err := summarizeNotionSearch(data)
	return tool.TextResult(content), err
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

func summarizeNotionSearch(data []byte) (string, error) {
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
		return "", fmt.Errorf("decode Notion search response: %w", err)
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
		return "no results", nil
	}
	return strings.Join(lines, "\n"), nil
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// GetPageTool reads the full text content of a Notion page.
type GetPageTool struct{ Client Client }

func (t GetPageTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"notion-get_page",
		"",
		tool.ObjectSchema([]string{"page_id"}, map[string]any{
			"page_id": map[string]any{"type": "string", "description": ""},
			"depth":   map[string]any{"type": "integer", "description": ""},
		}),
	)
}

func (t GetPageTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	if !t.Client.enabled() {
		return tool.Result{}, fmt.Errorf("Notion is not configured")
	}
	var args struct {
		PageID string `json:"page_id"`
		Depth  int    `json:"depth"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Depth <= 0 {
		args.Depth = 2
	}
	if args.Depth > 4 {
		args.Depth = 4
	}
	pageID, err := validateNotionID(args.PageID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("page_id: %w", err)
	}
	content, err := t.Client.fetchPageContent(ctx, pageID, args.Depth, 0)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(content), nil
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
				if err != nil {
					return "", err
				}
				if child != "" {
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

func (t QueryDatabaseTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"notion-query_database",
		"",
		tool.ObjectSchema([]string{"database_id"}, map[string]any{
			"database_id": map[string]any{"type": "string", "description": ""},
			"filter":      map[string]any{"type": "object", "description": ""},
			"sorts":       map[string]any{"type": "array", "description": ""},
			"limit":       map[string]any{"type": "integer", "description": ""},
		}),
	)
}

func (t QueryDatabaseTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	if !t.Client.enabled() {
		return tool.Result{}, fmt.Errorf("Notion is not configured")
	}
	var args struct {
		DatabaseID string          `json:"database_id"`
		Filter     json.RawMessage `json:"filter"`
		Sorts      json.RawMessage `json:"sorts"`
		Limit      int             `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Limit <= 0 {
		args.Limit = 20
	}
	if args.Limit > 100 {
		args.Limit = 100
	}
	dbID := strings.TrimSpace(args.DatabaseID)
	if dbID == "" {
		dbID = strings.TrimSpace(t.Client.DatabaseID)
	}
	dbID, err := validateNotionID(dbID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("database_id: %w", err)
	}

	body := map[string]any{"page_size": args.Limit}
	if len(args.Filter) > 0 && string(args.Filter) != "null" {
		var f any
		if err := json.Unmarshal(args.Filter, &f); err != nil {
			return tool.Result{}, fmt.Errorf("filter must be valid JSON: %w", err)
		}
		body["filter"] = f
	}
	if len(args.Sorts) > 0 && string(args.Sorts) != "null" {
		var s any
		if err := json.Unmarshal(args.Sorts, &s); err != nil {
			return tool.Result{}, fmt.Errorf("sorts must be valid JSON: %w", err)
		}
		body["sorts"] = s
	}

	url := fmt.Sprintf("https://api.notion.com/v1/databases/%s/query", dbID)
	data, err := t.Client.do(ctx, http.MethodPost, url, body)
	if err != nil {
		return tool.Result{}, err
	}
	content, err := summarizeNotionSearch(data)
	return tool.TextResult(content), err
}

func validateNotionID(input string) (string, error) {
	id := strings.TrimSpace(input)
	if id == "" {
		return "", fmt.Errorf("is required")
	}
	if strings.ContainsAny(id, "/?# \t\n\r") {
		return "", fmt.Errorf("must be an ID, not a URL or free-form value")
	}
	return id, nil
}

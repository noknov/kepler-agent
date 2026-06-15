package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

const (
	ProviderGoogleCSE = "google_cse"
	ProviderSerpAPI   = "serpapi"
)

type Client struct {
	Provider       string
	GoogleAPIKey   string
	GoogleCX       string
	SerpAPIKey     string
	SerpAPIBaseURL string
	HTTP           *http.Client
}

type ResultItem struct {
	Title   string
	URL     string
	Snippet string
	Source  string
}

type SearchTool struct {
	Client Client
}

func (SearchTool) Parallel() bool { return true }

func (t SearchTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"web-search",
		"",
		registry.ObjectSchema([]string{"query"}, map[string]any{
			"query":    map[string]any{"type": "string", "description": ""},
			"provider": map[string]any{"type": "string", "description": ""},
			"engine":   map[string]any{"type": "string", "description": ""},
			"site":     map[string]any{"type": "string", "description": ""},
			"limit":    map[string]any{"type": "integer", "description": ""},
		}),
	)
}

type ReadPageTool struct {
	Client Client
}

func (ReadPageTool) Parallel() bool { return true }

func (t ReadPageTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"web-read_page",
		"",
		registry.ObjectSchema([]string{"url"}, map[string]any{
			"url":       map[string]any{"type": "string", "description": ""},
			"max_chars": map[string]any{"type": "integer", "description": ""},
		}),
	)
}

func (t ReadPageTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	maxChars := args.MaxChars
	if maxChars <= 0 {
		maxChars = 12000
	}
	if maxChars > 50000 {
		maxChars = 50000
	}
	page, err := t.Client.ReadPage(ctx, args.URL, maxChars)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: formatPage(page)}, nil
}

func (t SearchTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Query    string `json:"query"`
		Provider string `json:"provider"`
		Engine   string `json:"engine"`
		Site     string `json:"site"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return registry.Result{}, fmt.Errorf("query is required")
	}
	if site := strings.TrimSpace(args.Site); site != "" {
		query += " site:" + site
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}
	items, err := t.Client.Search(ctx, SearchRequest{
		Query:    query,
		Provider: args.Provider,
		Engine:   args.Engine,
		Limit:    limit,
	})
	if err != nil {
		return registry.Result{}, err
	}
	if len(items) == 0 {
		return registry.Result{Content: "no web results"}, nil
	}
	return registry.Result{Content: formatResults(items)}, nil
}

type SearchRequest struct {
	Query    string
	Provider string
	Engine   string
	Limit    int
}

type Page struct {
	URL   string
	Title string
	Text  string
}

func (c Client) Search(ctx context.Context, req SearchRequest) ([]ResultItem, error) {
	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		provider = strings.TrimSpace(c.Provider)
	}
	if provider == "" {
		provider = ProviderGoogleCSE
	}
	switch provider {
	case ProviderGoogleCSE:
		return c.searchGoogleCSE(ctx, req)
	case ProviderSerpAPI:
		return c.searchSerpAPI(ctx, req)
	default:
		return nil, fmt.Errorf("unsupported web search provider %q", provider)
	}
}

func (c Client) ReadPage(ctx context.Context, pageURL string, maxChars int) (Page, error) {
	pageURL, err := normalizePageURL(pageURL)
	if err != nil {
		return Page{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return Page{}, err
	}
	req.Header.Set("Accept", "text/html, text/plain;q=0.9, application/xhtml+xml;q=0.8")
	req.Header.Set("User-Agent", "wati-oncall-agent/1.0")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Page{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Page{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Page{}, fmt.Errorf("web page status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.Contains(contentType, "text/html") && !strings.Contains(contentType, "text/plain") && !strings.Contains(contentType, "application/xhtml+xml") {
		return Page{}, fmt.Errorf("unsupported web page content type %q", contentType)
	}
	title, text := extractReadableText(string(data), contentType)
	if maxChars > 0 && len(text) > maxChars {
		text = truncateTextHeadTail(text, maxChars)
	}
	return Page{URL: pageURL, Title: title, Text: text}, nil
}

func (c Client) searchGoogleCSE(ctx context.Context, req SearchRequest) ([]ResultItem, error) {
	if strings.TrimSpace(c.GoogleAPIKey) == "" || strings.TrimSpace(c.GoogleCX) == "" {
		return nil, fmt.Errorf("Google web search is not configured: WEB_SEARCH_GOOGLE_API_KEY and WEB_SEARCH_GOOGLE_CX are required")
	}
	values := url.Values{}
	values.Set("key", c.GoogleAPIKey)
	values.Set("cx", c.GoogleCX)
	values.Set("q", req.Query)
	values.Set("num", fmt.Sprintf("%d", req.Limit))
	endpoint := "https://www.googleapis.com/customsearch/v1?" + values.Encode()
	var parsed struct {
		Items []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"items"`
	}
	if err := c.getJSON(ctx, endpoint, &parsed); err != nil {
		return nil, err
	}
	items := make([]ResultItem, 0, len(parsed.Items))
	for _, item := range parsed.Items {
		items = append(items, ResultItem{
			Title:   item.Title,
			URL:     item.Link,
			Snippet: item.Snippet,
			Source:  "google_cse",
		})
	}
	return items, nil
}

func (c Client) searchSerpAPI(ctx context.Context, req SearchRequest) ([]ResultItem, error) {
	if strings.TrimSpace(c.SerpAPIKey) == "" {
		return nil, fmt.Errorf("SerpAPI web search is not configured: WEB_SEARCH_SERPAPI_KEY is required")
	}
	engine := strings.TrimSpace(req.Engine)
	if engine == "" {
		engine = "google"
	}
	values := url.Values{}
	values.Set("api_key", c.SerpAPIKey)
	values.Set("engine", engine)
	values.Set("q", req.Query)
	values.Set("num", fmt.Sprintf("%d", req.Limit))
	endpoint := strings.TrimRight(c.SerpAPIBaseURL, "/")
	if endpoint == "" {
		endpoint = "https://serpapi.com/search.json"
	}
	endpoint += "?" + values.Encode()
	var parsed struct {
		OrganicResults []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic_results"`
	}
	if err := c.getJSON(ctx, endpoint, &parsed); err != nil {
		return nil, err
	}
	items := make([]ResultItem, 0, len(parsed.OrganicResults))
	for _, item := range parsed.OrganicResults {
		items = append(items, ResultItem{
			Title:   item.Title,
			URL:     item.Link,
			Snippet: item.Snippet,
			Source:  "serpapi/" + engine,
		})
	}
	return items, nil
}

func (c Client) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("web search status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	return nil
}

func normalizePageURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported URL scheme %q; only http and https are allowed", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("url host is required")
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

func extractReadableText(data, contentType string) (string, string) {
	if strings.Contains(contentType, "text/plain") {
		return "", cleanWhitespace(data)
	}
	title := extractTitle(data)
	text := data
	text = regexp.MustCompile(`(?is)<head[^>]*>.*?</head>`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`(?is)<noscript[^>]*>.*?</noscript>`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`(?is)<!--.*?-->`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`(?i)<br\s*/?>`).ReplaceAllString(text, "\n")
	text = regexp.MustCompile(`(?i)</(p|div|section|article|li|h[1-6]|tr)>`).ReplaceAllString(text, "\n")
	text = regexp.MustCompile(`(?s)<[^>]+>`).ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	return title, cleanWhitespace(text)
}

func extractTitle(data string) string {
	match := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindStringSubmatch(data)
	if len(match) < 2 {
		return ""
	}
	return cleanWhitespace(html.UnescapeString(match[1]))
}

func cleanWhitespace(text string) string {
	lines := strings.Split(text, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
		line = regexp.MustCompile(`\s+([.,;:!?，。；：！？])`).ReplaceAllString(line, "$1")
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, "\n")
}

func truncateTextHeadTail(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	marker := "\n...[middle truncated to fit page read budget]...\n"
	if max <= len(marker)+200 {
		return strings.TrimSpace(text[:max]) + "\n...[truncated]"
	}
	keep := max - len(marker)
	head := keep / 2
	tail := keep - head
	return strings.TrimSpace(text[:head]) + marker + strings.TrimSpace(text[len(text)-tail:])
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func formatResults(items []ResultItem) string {
	var b strings.Builder
	b.WriteString("Web search results\n")
	for i, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = "untitled"
		}
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, title))
		if item.URL != "" {
			b.WriteString("   url: " + item.URL + "\n")
		}
		if item.Source != "" {
			b.WriteString("   source: " + item.Source + "\n")
		}
		if strings.TrimSpace(item.Snippet) != "" {
			b.WriteString("   snippet: " + strings.Join(strings.Fields(item.Snippet), " ") + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func formatPage(page Page) string {
	var b strings.Builder
	b.WriteString("Web page\n")
	b.WriteString("url: " + page.URL + "\n")
	if strings.TrimSpace(page.Title) != "" {
		b.WriteString("title: " + page.Title + "\n")
	}
	if strings.TrimSpace(page.Text) != "" {
		b.WriteString("\n" + page.Text)
	}
	return strings.TrimSpace(b.String())
}

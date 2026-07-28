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

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

const (
	ProviderGoogleCSE  = "google_cse"
	ProviderSerpAPI    = "serpapi"
	ProviderDuckDuckGo = "duckduckgo"
	ProviderSearXNG    = "searxng"
	ProviderBrave      = "brave"
)

type Client struct {
	Provider       string
	GoogleAPIKey   string
	GoogleCX       string
	SerpAPIKey     string
	SerpAPIBaseURL string
	SearXNGBaseURL string
	BraveAPIKey    string
	BraveBaseURL   string
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
		"Search the public web for current information. Prefer this before web-read_page when the user needs recent facts, recommendations, admissions data, prices, policies, news, or other information beyond model memory. Return URLs in the final answer for claims based on search results.",
		registry.ObjectSchema([]string{"query"}, map[string]any{
			"query":    map[string]any{"type": "string", "description": "Search query. Include the current year for current or time-sensitive information."},
			"provider": map[string]any{"type": "string", "description": "Optional provider: brave, duckduckgo, searxng, google_cse, or serpapi."},
			"engine":   map[string]any{"type": "string", "description": "Optional provider-specific engine, such as baidu for serpapi."},
			"site":     map[string]any{"type": "string", "description": "Optional domain filter. Example: hbea.edu.cn"},
			"limit":    map[string]any{"type": "integer", "description": "Maximum results to return, default 5 and max 10."},
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
		"Read a public URL and extract readable text. Use after web-search returns a promising result, when the user provides a URL, or when you already know a stable source URL and search failed or is unnecessary. Search result pages are weak evidence; prefer reading the actual source pages.",
		registry.ObjectSchema([]string{"url"}, map[string]any{
			"url":       map[string]any{"type": "string", "description": "HTTP or HTTPS URL to read."},
			"max_chars": map[string]any{"type": "integer", "description": "Maximum extracted characters, default 12000 and max 50000."},
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
		return registry.Result{Content: searchFailureGuidance(query, err)}, nil
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
		provider = ProviderDuckDuckGo
	}
	switch provider {
	case ProviderGoogleCSE:
		return c.searchGoogleCSE(ctx, req)
	case ProviderSerpAPI:
		return c.searchSerpAPI(ctx, req)
	case ProviderDuckDuckGo:
		return c.searchDuckDuckGo(ctx, req)
	case ProviderSearXNG:
		return c.searchSearXNG(ctx, req)
	case ProviderBrave:
		return c.searchBrave(ctx, req)
	default:
		return nil, fmt.Errorf("unsupported web search provider %q", provider)
	}
}

func searchFailureGuidance(query string, err error) string {
	var b strings.Builder
	b.WriteString("Web search provider failed: ")
	b.WriteString(err.Error())
	b.WriteString("\n\n")
	b.WriteString("Search failure does not mean direct web reading is unavailable. If the answer can be obtained from a known stable source URL, call web-read_page directly instead of telling the user that live information is unavailable.")
	b.WriteString("\n\n")
	b.WriteString("For weather/current-condition questions, direct text pages such as https://wttr.in/{city}?format=3 are readable without search; replace {city} with the requested city name, then answer from the page content.")
	if strings.TrimSpace(query) != "" {
		b.WriteString("\n\nOriginal query: ")
		b.WriteString(query)
	}
	return b.String()
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
	req.Header.Set("User-Agent", "slack-copilot-agent/1.0")
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

func (c Client) searchDuckDuckGo(ctx context.Context, req SearchRequest) ([]ResultItem, error) {
	values := url.Values{}
	values.Set("q", req.Query)
	endpoint := "https://html.duckduckgo.com/html/?" + values.Encode()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/html, application/xhtml+xml;q=0.9")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; slack-copilot-agent/1.0)")
	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("duckduckgo search status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return parseDuckDuckGoHTML(string(data), req.Limit), nil
}

func (c Client) searchSearXNG(ctx context.Context, req SearchRequest) ([]ResultItem, error) {
	base := strings.TrimRight(strings.TrimSpace(c.SearXNGBaseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("SearXNG web search is not configured: WEB_SEARCH_SEARXNG_URL is required")
	}
	values := url.Values{}
	values.Set("q", req.Query)
	values.Set("format", "json")
	endpoint := base + "/search?" + values.Encode()
	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := c.getJSON(ctx, endpoint, &parsed); err != nil {
		return nil, err
	}
	items := make([]ResultItem, 0, len(parsed.Results))
	for _, item := range parsed.Results {
		items = append(items, ResultItem{
			Title:   cleanWhitespace(html.UnescapeString(item.Title)),
			URL:     item.URL,
			Snippet: cleanWhitespace(html.UnescapeString(item.Content)),
			Source:  "searxng",
		})
	}
	return limitItems(items, req.Limit), nil
}

func (c Client) searchBrave(ctx context.Context, req SearchRequest) ([]ResultItem, error) {
	if strings.TrimSpace(c.BraveAPIKey) == "" {
		return nil, fmt.Errorf("Brave web search is not configured: WEB_SEARCH_BRAVE_API_KEY is required")
	}
	values := url.Values{}
	values.Set("q", req.Query)
	values.Set("count", fmt.Sprintf("%d", req.Limit))
	endpoint := strings.TrimSpace(c.BraveBaseURL)
	if endpoint == "" {
		endpoint = "https://api.search.brave.com/res/v1/web/search"
	}
	endpoint = strings.TrimRight(endpoint, "/") + "?" + values.Encode()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Subscription-Token", c.BraveAPIKey)
	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("brave search status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	items := make([]ResultItem, 0, len(parsed.Web.Results))
	for _, item := range parsed.Web.Results {
		items = append(items, ResultItem{
			Title:   cleanWhitespace(html.UnescapeString(item.Title)),
			URL:     item.URL,
			Snippet: cleanWhitespace(html.UnescapeString(item.Description)),
			Source:  "brave",
		})
	}
	return limitItems(items, req.Limit), nil
}

func parseDuckDuckGoHTML(data string, limit int) []ResultItem {
	blockRe := regexp.MustCompile(`(?is)<div[^>]+class="[^"]*result[^"]*"[^>]*>(.*?)</div>\s*</div>`)
	linkRe := regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__a[^"]*"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	snippetRe := regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>|<div[^>]+class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</div>`)
	blocks := blockRe.FindAllStringSubmatch(data, -1)
	items := make([]ResultItem, 0, len(blocks))
	for _, block := range blocks {
		htmlBlock := block[1]
		link := linkRe.FindStringSubmatch(htmlBlock)
		if len(link) < 3 {
			continue
		}
		resultURL := decodeDuckDuckGoURL(html.UnescapeString(link[1]))
		if resultURL == "" {
			continue
		}
		snippet := ""
		if match := snippetRe.FindStringSubmatch(htmlBlock); len(match) > 0 {
			for _, part := range match[1:] {
				if strings.TrimSpace(part) != "" {
					snippet = htmlToText(part)
					break
				}
			}
		}
		items = append(items, ResultItem{
			Title:   htmlToText(link[2]),
			URL:     resultURL,
			Snippet: snippet,
			Source:  "duckduckgo",
		})
	}
	return limitItems(items, limit)
}

func decodeDuckDuckGoURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err == nil {
		if uddg := parsed.Query().Get("uddg"); uddg != "" {
			return uddg
		}
		if parsed.Scheme == "http" || parsed.Scheme == "https" {
			return parsed.String()
		}
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	return raw
}

func htmlToText(raw string) string {
	text := regexp.MustCompile(`(?s)<[^>]+>`).ReplaceAllString(raw, " ")
	return cleanWhitespace(html.UnescapeString(text))
}

func limitItems(items []ResultItem, limit int) []ResultItem {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[:limit]
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
	if err := safety.ValidatePublicHTTPURL(parsed.String()); err != nil {
		return "", err
	}
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
	return safety.SafeHTTPClient(15 * time.Second)
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

package websearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
)

func TestGoogleCSEResults(t *testing.T) {
	client := Client{
		Provider:     ProviderGoogleCSE,
		GoogleAPIKey: "key",
		GoogleCX:     "cx",
		HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.URL.Query().Get("q"); got != "slack copilot agent" {
				t.Fatalf("q = %q", got)
			}
			return jsonResponse(`{"items":[{"title":"Result","link":"https://example.com","snippet":"hello world"}]}`), nil
		})},
	}
	items, err := client.Search(context.Background(), SearchRequest{Query: "slack copilot agent", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Source != "google_cse" || items[0].URL != "https://example.com" {
		t.Fatalf("items = %#v", items)
	}
}

func TestSerpAPIBaiduResults(t *testing.T) {
	client := Client{
		Provider:       ProviderSerpAPI,
		SerpAPIKey:     "key",
		SerpAPIBaseURL: "https://serpapi.test/search.json",
		HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.URL.Query().Get("engine"); got != "baidu" {
				t.Fatalf("engine = %q", got)
			}
			return jsonResponse(`{"organic_results":[{"title":"百度结果","link":"https://example.cn","snippet":"摘要"}]}`), nil
		})},
	}
	items, err := client.Search(context.Background(), SearchRequest{Query: "故障", Engine: "baidu", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Source != "serpapi/baidu" || items[0].Title != "百度结果" {
		t.Fatalf("items = %#v", items)
	}
}

func TestDuckDuckGoHTMLResults(t *testing.T) {
	client := Client{
		Provider: ProviderDuckDuckGo,
		HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "html.duckduckgo.com" {
				t.Fatalf("host = %q", req.URL.Host)
			}
			if got := req.URL.Query().Get("q"); got != "湖北 高考" {
				t.Fatalf("q = %q", got)
			}
			return htmlResponse(`
				<div class="result">
				  <h2><a class="result__a" href="/l/?uddg=https%3A%2F%2Fexample.edu%2Fpage&amp;rut=x">湖北省教育厅</a></h2>
				  <a class="result__snippet">一分一段表和录取控制分数线</a>
				</div></div>
			`), nil
		})},
	}
	items, err := client.Search(context.Background(), SearchRequest{Query: "湖北 高考", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Source != "duckduckgo" || items[0].URL != "https://example.edu/page" || items[0].Title != "湖北省教育厅" {
		t.Fatalf("items = %#v", items)
	}
}

func TestSearXNGResults(t *testing.T) {
	client := Client{
		Provider:       ProviderSearXNG,
		SearXNGBaseURL: "http://searxng.test",
		HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != "http://searxng.test/search?format=json&q=slack+copilot+agent" {
				t.Fatalf("url = %q", req.URL.String())
			}
			return jsonResponse(`{"results":[{"title":"Result","url":"https://example.com","content":"hello"}]}`), nil
		})},
	}
	items, err := client.Search(context.Background(), SearchRequest{Query: "slack copilot agent", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Source != "searxng" || items[0].Snippet != "hello" {
		t.Fatalf("items = %#v", items)
	}
}

func TestBraveResults(t *testing.T) {
	client := Client{
		Provider:     ProviderBrave,
		BraveAPIKey:  "brave-key",
		BraveBaseURL: "https://brave.test/res/v1/web/search",
		HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("X-Subscription-Token"); got != "brave-key" {
				t.Fatalf("subscription token = %q", got)
			}
			if got := req.URL.Query().Get("q"); got != "slack copilot agent" {
				t.Fatalf("q = %q", got)
			}
			if got := req.URL.Query().Get("count"); got != "3" {
				t.Fatalf("count = %q", got)
			}
			return jsonResponse(`{"web":{"results":[{"title":"Result","url":"https://example.com","description":"hello brave"}]}}`), nil
		})},
	}
	items, err := client.Search(context.Background(), SearchRequest{Query: "slack copilot agent", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Source != "brave" || items[0].Snippet != "hello brave" {
		t.Fatalf("items = %#v", items)
	}
}

func TestSearchToolFailureGuidesDirectPageRead(t *testing.T) {
	client := Client{
		Provider:       ProviderSearXNG,
		SearXNGBaseURL: "http://searxng.test",
		HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "searxng.test" {
				t.Fatalf("unexpected host = %q", req.URL.Host)
			}
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("down")),
			}, nil
		})},
	}
	result, err := (SearchTool{Client: client}).Execute(
		context.Background(),
		json.RawMessage(`{"query":"深圳天气","limit":3}`),
		registry.Runtime{},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Web search provider failed", "web-read_page", "https://wttr.in/{city}?format=3", "深圳天气"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("result content missing %q: %s", want, result.Content)
		}
	}
}

func TestMissingProviderConfigReturnsClearError(t *testing.T) {
	_, err := (Client{Provider: ProviderGoogleCSE}).Search(context.Background(), SearchRequest{Query: "x", Limit: 1})
	if err == nil || !strings.Contains(err.Error(), "WEB_SEARCH_GOOGLE_API_KEY") {
		t.Fatalf("error = %v, want missing config", err)
	}
}

func TestReadPageExtractsHTMLText(t *testing.T) {
	client := Client{HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://example.com/doc?a=1" {
			t.Fatalf("url = %q", req.URL.String())
		}
		return htmlResponse(`<html><head><title>Example &amp; Docs</title><style>.x{}</style><script>alert(1)</script></head><body><h1>Hello</h1><p>Readable <b>text</b>.</p></body></html>`), nil
	})}}
	page, err := client.ReadPage(context.Background(), "https://example.com/doc?a=1#section", 10000)
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != "https://example.com/doc?a=1" {
		t.Fatalf("page URL = %q", page.URL)
	}
	if page.Title != "Example & Docs" {
		t.Fatalf("title = %q", page.Title)
	}
	if !strings.Contains(page.Text, "Hello") || !strings.Contains(page.Text, "Readable text.") || strings.Contains(page.Text, "alert") {
		t.Fatalf("text = %q", page.Text)
	}
}

func TestReadPageRejectsUnsafeScheme(t *testing.T) {
	_, err := (Client{}).ReadPage(context.Background(), "file:///etc/passwd", 1000)
	if err == nil || !strings.Contains(err.Error(), "only http and https") {
		t.Fatalf("error = %v, want scheme rejection", err)
	}
}

func TestReadPageRejectsUnsupportedContentType(t *testing.T) {
	client := Client{HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/octet-stream"}},
			Body:       io.NopCloser(strings.NewReader("binary")),
		}, nil
	})}}
	_, err := client.ReadPage(context.Background(), "https://example.com/file.bin", 1000)
	if err == nil || !strings.Contains(err.Error(), "unsupported web page content type") {
		t.Fatalf("error = %v, want content type rejection", err)
	}
}

func TestReadPageTruncatesHeadAndTail(t *testing.T) {
	client := Client{HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return textResponse(strings.Repeat("A", 1000) + "\n" + strings.Repeat("Z", 1000)), nil
	})}}
	page, err := client.ReadPage(context.Background(), "https://example.com/long.txt", 600)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page.Text, strings.Repeat("A", 100)) || !strings.Contains(page.Text, strings.Repeat("Z", 100)) || !strings.Contains(page.Text, "middle truncated") {
		t.Fatalf("text = %q", page.Text)
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func htmlResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func textResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func BenchmarkDeferredSearch(b *testing.B) {
	catalog, err := NewCatalog()
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if err := catalog.Register(namedDeferredFixture{name: fmt.Sprintf("integration-search-%03d", i), description: "Search connected documents, logs and deployment metrics."}); err != nil {
			b.Fatal(err)
		}
	}
	search := searchTool{catalog: catalog}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := search.search("搜索文档和部署日志 search document deployment logs", 8); err != nil {
			b.Fatal(err)
		}
	}
}

func TestSearchHelpersPreserveUnicodeAndDeduplicate(t *testing.T) {
	got := compactStrings([]string{"b", "a", "b", " a ", ""})
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("names = %v", got)
	}
	gotText := trimDescription(strings.Repeat("文", 121), 120)
	if !utf8.ValidString(gotText) || gotText != strings.Repeat("文", 120)+"..." {
		t.Fatalf("description = %q", gotText)
	}
}

type deferredFixture struct{}

func (deferredFixture) Descriptor() Descriptor {
	return Descriptor{Name: "later-tool", Description: "deferred fixture", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []Effect{EffectRead}, Exposure: ExposureDeferred, Tags: []string{CategoryIntegration}}
}
func (deferredFixture) Execute(context.Context, Call) (Result, error) { return TextResult("ok"), nil }

func TestSearchToolListsAndActivates(t *testing.T) {
	catalog, err := NewCatalog(deferredFixture{})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Register(NewSearchTool(catalog)); err != nil {
		t.Fatal(err)
	}
	search, ok := catalog.GetActive("s1", "tool_search")
	if !ok {
		t.Fatal("tool_search missing")
	}
	list, err := search.Execute(context.Background(), Call{Name: "tool_search", Arguments: json.RawMessage(`{"action":"list"}`), Scope: Scope{SessionID: "s1", TurnID: "t1"}})
	if err != nil || list.Text() == "" {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	found, err := search.Execute(context.Background(), Call{Name: "tool_search", Arguments: json.RawMessage(`{"action":"search","query":"integration"}`), Scope: Scope{SessionID: "s1", TurnID: "t1"}})
	if err != nil || found.Text() == "" {
		t.Fatalf("search=%+v err=%v", found, err)
	}
	activate, err := search.Execute(context.Background(), Call{Name: "tool_search", Arguments: json.RawMessage(`{"action":"activate","tool_names":["later-tool"]}`), Scope: Scope{SessionID: "s1", TurnID: "t1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.GetActive("s1", "later-tool"); !ok {
		t.Fatalf("activation failed: %s", activate.Text())
	}
}

type namedDeferredFixture struct {
	name        string
	description string
}

func (f namedDeferredFixture) Descriptor() Descriptor {
	return Descriptor{Name: f.name, Description: f.description, InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []Effect{EffectRead}, Exposure: ExposureDeferred, Tags: []string{CategoryIntegration}}
}

func (namedDeferredFixture) Execute(context.Context, Call) (Result, error) {
	return TextResult("ok"), nil
}

func TestSearchToolPrioritizesExactToolNameMatches(t *testing.T) {
	catalog, err := NewCatalog(
		namedDeferredFixture{name: "generic-review", description: "Read Notion pages and YouTrack issues from links, access project documentation and tickets for review."},
		namedDeferredFixture{name: "mcp_notion_notion-search", description: "Search connected Notion pages and databases."},
		namedDeferredFixture{name: "youtrack-get_issue", description: "Read a YouTrack issue by ID."},
	)
	if err != nil {
		t.Fatal(err)
	}
	search := NewSearchTool(catalog)
	result, err := search.Execute(context.Background(), Call{
		Name:      "tool_search",
		Arguments: json.RawMessage(`{"action":"search","query":"read Notion pages and YouTrack issues from links","limit":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := result.Text()
	if !strings.Contains(text, "mcp_notion_notion-search") || !strings.Contains(text, "youtrack-get_issue") {
		t.Fatalf("exactly named integrations were not prioritized: %s", text)
	}
	if strings.Contains(text, "generic-review") {
		t.Fatalf("generic description match outranked named integrations: %s", text)
	}
}

func TestSearchToolExpandsCommonChineseCapabilityTerms(t *testing.T) {
	tokens := strings.Join(queryTokens("搜索文档并创建邮件"), " ")
	for _, want := range []string{"search", "document", "create", "email"} {
		if !strings.Contains(tokens, want) {
			t.Fatalf("tokens=%q missing %q", tokens, want)
		}
	}
}

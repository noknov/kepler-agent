package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSearchQueryCannotActivateTools(t *testing.T) {
	catalog, _ := NewCatalog(deferredFixture{})
	search := NewSearchTool(catalog)
	for _, query := range []string{"select:later-tool", "SELECT: later-tool"} {
		args, _ := json.Marshal(map[string]string{"action": "search", "query": query})
		_, err := search.Execute(t.Context(), Call{Arguments: args, Scope: Scope{SessionID: "session", TurnID: "turn"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, active := catalog.GetActive("session", "later-tool"); active {
			t.Fatalf("search query changed tool visibility: %q", query)
		}
	}
}

func TestSearchDoesNotInventSubstringTermMatches(t *testing.T) {
	catalog, _ := NewCatalog(namedDeferredFixture{name: "together", description: "Inspect build artifacts."})
	result, err := NewSearchTool(catalog).Execute(t.Context(), Call{Arguments: json.RawMessage(`{"action":"search","query":"get"}`)})
	if err != nil || strings.Contains(result.Text(), "together") {
		t.Fatalf("invented a get term inside an unrelated identifier: %q err=%v", result.Text(), err)
	}
}

func TestSearchExactIdentityIncludesShortRegisteredNames(t *testing.T) {
	catalog, _ := NewCatalog(namedDeferredFixture{name: "x", description: "Inspect artifacts."})
	result, err := NewSearchTool(catalog).Execute(t.Context(), Call{Arguments: json.RawMessage(`{"action":"search","query":"x"}`)})
	if err != nil || !strings.Contains(result.Text(), "x [") {
		t.Fatalf("registered identity was lost to tokenization: %q err=%v", result.Text(), err)
	}
}

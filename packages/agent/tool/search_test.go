package tool

import (
	"context"
	"encoding/json"
	"testing"
)

type deferredFixture struct{}

func (deferredFixture) Descriptor() Descriptor {
	return Descriptor{Name: "later-tool", Description: "deferred fixture", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []Effect{EffectRead}, Exposure: ExposureDeferred, Tags: []string{CategoryCode}}
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
	found, err := search.Execute(context.Background(), Call{Name: "tool_search", Arguments: json.RawMessage(`{"action":"search","query":"code graph"}`), Scope: Scope{SessionID: "s1", TurnID: "t1"}})
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

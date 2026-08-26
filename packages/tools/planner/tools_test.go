package planner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
)

func TestPlanUpdateStoresAndFormatsPlan(t *testing.T) {
	scope := agenttool.Scope{SessionID: "test", TurnID: "turn"}
	result, err := (PlanTool{}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{
		"summary":"start complex work",
		"items":[
			{"id":"1","task":"Inspect architecture","status":"completed"},
			{"id":"2","task":"Implement changes","status":"in_progress","note":"editing core loop"}
		]
	}`), Scope: scope})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Text(), "Plan update: start complex work") ||
		!strings.Contains(result.Text(), "2 [in_progress] Implement changes - editing core loop") {
		t.Fatalf("unexpected content: %q", result.Text())
	}
	cached, ok := agenttool.CacheFor(scope).Get(cacheKey)
	if !ok {
		t.Fatal("plan was not cached")
	}
	items := cached.([]Item)
	if len(items) != 2 || items[1].Status != "in_progress" {
		t.Fatalf("cached plan = %#v", items)
	}
}

func TestPlanUpdateRejectsMultipleInProgressItems(t *testing.T) {
	_, err := (PlanTool{}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{
		"items":[
			{"task":"One","status":"in_progress"},
			{"task":"Two","status":"in_progress"}
		]
	}`), Scope: agenttool.Scope{SessionID: "test", TurnID: "turn"}})
	if err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("error = %v, want at most one in_progress", err)
	}
}

package planner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
)

func TestPlanUpdateReturnsStructuredPlan(t *testing.T) {
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
	if result.Text() != "Plan updated" || result.Plan == nil || result.Plan.Explanation != "start complex work" {
		t.Fatalf("unexpected result: %#v", result)
	}
	cached, ok := agenttool.CacheFor(scope).Get(cacheKey)
	if !ok {
		t.Fatal("plan was not cached")
	}
	plan := cached.(agenttool.PlanUpdate)
	if len(plan.Items) != 2 || plan.Items[1].Status != "in_progress" {
		t.Fatalf("cached plan = %#v", plan)
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

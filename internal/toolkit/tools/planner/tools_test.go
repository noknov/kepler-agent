package planner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

func TestPlanUpdateStoresAndFormatsPlan(t *testing.T) {
	rt := registry.Runtime{Cache: registry.NewRuntimeCache()}
	result, err := (PlanTool{}).Execute(context.Background(), json.RawMessage(`{
		"summary":"start complex work",
		"items":[
			{"id":"1","task":"Inspect architecture","status":"completed"},
			{"id":"2","task":"Implement changes","status":"in_progress","note":"editing core loop"}
		]
	}`), rt)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "Plan update: start complex work") ||
		!strings.Contains(result.Content, "2 [in_progress] Implement changes - editing core loop") {
		t.Fatalf("unexpected content: %q", result.Content)
	}
	cached, ok := rt.Cache.Get(cacheKey)
	if !ok {
		t.Fatal("plan was not cached")
	}
	items := cached.([]Item)
	if len(items) != 2 || items[1].Status != "in_progress" {
		t.Fatalf("cached plan = %#v", items)
	}
}

func TestPlanUpdateRejectsMultipleInProgressItems(t *testing.T) {
	_, err := (PlanTool{}).Execute(context.Background(), json.RawMessage(`{
		"items":[
			{"task":"One","status":"in_progress"},
			{"task":"Two","status":"in_progress"}
		]
	}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("error = %v, want at most one in_progress", err)
	}
}

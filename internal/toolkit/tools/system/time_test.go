package system

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

func TestCurrentTimeToolReturnsSystemClockFields(t *testing.T) {
	result, err := CurrentTimeTool{}.Execute(context.Background(), nil, registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, result.Content)
	}
	for _, key := range []string{"date", "time", "datetime", "timezone", "utc_datetime", "unix_timestamp"} {
		if payload[key] == nil {
			t.Fatalf("missing %s in payload %#v", key, payload)
		}
	}
}

package system

import (
	"context"
	"encoding/json"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type CurrentTimeTool struct{}

func (CurrentTimeTool) Repeatable() bool { return true }

func (CurrentTimeTool) Parallel() bool { return true }

func (CurrentTimeTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"system-current_time",
		"",
		registry.ObjectSchema(nil, map[string]any{}),
	)
}

func (CurrentTimeTool) Execute(context.Context, json.RawMessage, registry.Runtime) (registry.Result, error) {
	now := time.Now()
	utc := now.UTC()
	payload := map[string]any{
		"date":           now.Format("2006-01-02"),
		"time":           now.Format("15:04:05"),
		"datetime":       now.Format(time.RFC3339),
		"timezone":       now.Location().String(),
		"utc_datetime":   utc.Format(time.RFC3339),
		"unix_timestamp": now.Unix(),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: string(data)}, nil
}

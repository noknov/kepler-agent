package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

const (
	maxToolResultChars = 15000
	previewChars       = 2000
	spillReadLimit     = 4000
	maxSpillReadLimit  = 12000
)

func spillToolResult(ctx context.Context, store registry.ToolSpillStore, runID, toolName, toolCallID, content string) (string, error) {
	if store == nil {
		return "", fmt.Errorf("PostgreSQL tool spill store is required")
	}
	if err := store.SaveToolSpill(ctx, spillRunID(runID), toolName, toolCallID, content); err != nil {
		return "", err
	}
	return spillNotice(toolName, toolCallID, content), nil
}

func spillNotice(toolName, toolCallID, content string) string {
	preview := content
	if len([]rune(preview)) > previewChars {
		preview = string([]rune(preview)[:previewChars])
	}
	return fmt.Sprintf("<persisted-output>\nOutput too large (%d chars); showing first %d chars. To inspect more, call tool_spill-read with tool_name=%q and tool_call_id=%q. Prefer query for targeted evidence, or offset/limit for a bounded slice.\n\n%s\n...\n</persisted-output>",
		len(content), len(preview), toolName, toolCallID, preview)
}

func maybeSpillResult(ctx context.Context, store registry.ToolSpillStore, runID, toolName, toolCallID, content string) string {
	if len(content) <= maxToolResultChars {
		return content
	}
	spilled, err := spillToolResult(ctx, store, runID, toolName, toolCallID, content)
	if err != nil {
		return truncateRunes(content, maxToolResultChars) + "\n\n[truncated]"
	}
	return spilled
}

func truncateRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max])
}

func spillRunID(runID string) string {
	runID = strings.TrimSpace(runID)
	if runID != "" {
		return runID
	}
	return "unknown"
}

type SpillReadTool struct{}

func (SpillReadTool) Parallel() bool { return true }

func (SpillReadTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"tool_spill-read",
		"Read a bounded slice from a large tool output that was persisted by a previous tool call in this run. Use query to jump near matching text, or offset/limit for pagination.",
		registry.ObjectSchema([]string{"tool_name", "tool_call_id"}, map[string]any{
			"tool_name":    map[string]any{"type": "string", "description": "Name of the tool whose output was persisted."},
			"tool_call_id": map[string]any{"type": "string", "description": "Tool call ID from the persisted-output notice."},
			"query":        map[string]any{"type": "string", "description": "Optional case-insensitive text to locate within the persisted output."},
			"offset":       map[string]any{"type": "integer", "description": "Zero-based character offset to read from when query is omitted or not found."},
			"limit":        map[string]any{"type": "integer", "description": "Maximum characters to return. Defaults to 4000, max 12000."},
		}),
	)
}

func (SpillReadTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		ToolName   string `json:"tool_name"`
		ToolCallID string `json:"tool_call_id"`
		Query      string `json:"query"`
		Offset     int    `json:"offset"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	args.ToolName = strings.TrimSpace(args.ToolName)
	args.ToolCallID = strings.TrimSpace(args.ToolCallID)
	if args.ToolName == "" {
		return registry.Result{}, fmt.Errorf("tool_name is required")
	}
	if args.ToolCallID == "" {
		return registry.Result{}, fmt.Errorf("tool_call_id is required")
	}
	limit := args.Limit
	if limit <= 0 {
		limit = spillReadLimit
	}
	if limit > maxSpillReadLimit {
		limit = maxSpillReadLimit
	}
	if rt.ToolSpillStore == nil {
		return registry.Result{}, fmt.Errorf("PostgreSQL tool spill store is required")
	}
	rawContent, err := rt.ToolSpillStore.ReadToolSpill(ctx, spillRunID(rt.RunID), args.ToolName, args.ToolCallID)
	if err != nil {
		return registry.Result{}, fmt.Errorf("persisted output not found for tool_name=%q tool_call_id=%q", args.ToolName, args.ToolCallID)
	}
	content := []rune(rawContent)
	offset := args.Offset
	queryNotice := ""
	if query := strings.TrimSpace(args.Query); query != "" {
		lowerContent := strings.ToLower(string(content))
		idx := strings.Index(lowerContent, strings.ToLower(query))
		if idx >= 0 {
			runeIndex := len([]rune(lowerContent[:idx]))
			offset = runeIndex - limit/4
			queryNotice = fmt.Sprintf("Query %q found near character %d.\n", query, runeIndex)
		} else {
			queryNotice = fmt.Sprintf("Query %q was not found; returning requested offset instead.\n", query)
		}
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(content) {
		offset = len(content)
	}
	end := offset + limit
	if end > len(content) {
		end = len(content)
	}
	next := ""
	if end < len(content) {
		next = fmt.Sprintf("\n\n[next_offset=%d]", end)
	}
	body := string(content[offset:end])
	return registry.Result{Content: fmt.Sprintf("Persisted output slice for %s/%s chars %d:%d of %d.\n%s\n%s%s", args.ToolName, args.ToolCallID, offset, end, len(content), queryNotice, body, next)}, nil
}

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

const (
	maxToolResultChars = 15000
	previewChars       = 2000
	spillReadLimit     = 4000
	maxSpillReadLimit  = 12000
	spillDir           = ".data/tool-spill"
)

var unsafeSpillNameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func spillToolResult(runID, toolName, toolCallID, content string) (string, error) {
	dir := filepath.Join(spillDir, runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	path := spillPath(runID, toolName, toolCallID)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}

	preview := content
	if len([]rune(preview)) > previewChars {
		preview = string([]rune(preview)[:previewChars])
	}
	return fmt.Sprintf("<persisted-output>\nOutput too large (%d chars); showing first %d chars. To inspect more, call tool_spill-read with tool_name=%q and tool_call_id=%q. Prefer query for targeted evidence, or offset/limit for a bounded slice.\n\n%s\n...\n</persisted-output>",
		len(content), len(preview), toolName, toolCallID, preview), nil
}

func maybeSpillResult(runID, toolName, toolCallID, content string) string {
	if len(content) <= maxToolResultChars {
		return content
	}
	spilled, err := spillToolResult(runID, toolName, toolCallID, content)
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

func spillPath(runID, toolName, toolCallID string) string {
	return filepath.Join(spillDir, spillRunID(runID), spillFilename(toolName, toolCallID))
}

func spillFilename(toolName, toolCallID string) string {
	suffix := sanitizeSpillName(toolCallID)
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	if suffix == "" {
		suffix = "result"
	}
	return fmt.Sprintf("%s-%s.txt", sanitizeSpillName(toolName), suffix)
}

func sanitizeSpillName(name string) string {
	name = strings.TrimSpace(name)
	name = unsafeSpillNameChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, ".-")
	if name == "" {
		return "unknown"
	}
	return name
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

func (SpillReadTool) Execute(_ context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
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
	data, err := os.ReadFile(spillPath(rt.RunID, args.ToolName, args.ToolCallID))
	if err != nil {
		return registry.Result{}, fmt.Errorf("persisted output not found for tool_name=%q tool_call_id=%q", args.ToolName, args.ToolCallID)
	}
	content := []rune(string(data))
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

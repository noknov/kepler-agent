package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

const assistantWrapIndent = "\n     "

type eventRenderer struct {
	mode           string
	stdout, stderr io.Writer
	mu             sync.Mutex
	color          bool
	started        map[string]time.Time
	streamed       bool
	toolActivity   bool
	waiting        *waitSpinner
}

func newEventRenderer(mode string, stdout, stderr io.Writer) *eventRenderer {
	if mode == "" {
		mode = "text"
	}
	return &eventRenderer{
		mode:    mode,
		stdout:  stdout,
		stderr:  stderr,
		color:   mode == "text" && isTerminal(os.Stderr),
		started: make(map[string]time.Time),
	}
}

func (r *eventRenderer) Publish(_ context.Context, event transcript.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mode == "jsonl" {
		data, _ := json.Marshal(event)
		fmt.Fprintln(r.stdout, string(data))
		return
	}
	if event.Type == transcript.ModelStreamed && event.Model != nil && event.Model.Type == model.StreamTextDelta {
		r.stopWaitLocked()
		if !r.streamed {
			gap := "\n"
			if !r.toolActivity {
				gap = "\n\n"
			}
			fmt.Fprint(r.stdout, gap+r.paint("⎿", colorDim)+" ")
			r.streamed = true
		}
		fmt.Fprint(r.stdout, strings.ReplaceAll(event.Model.Text, "\n", assistantWrapIndent))
	}
	if event.Type == transcript.ToolCallStarted && event.ToolCall != nil {
		r.stopWaitLocked()
		r.toolActivity = true
		if r.streamed {
			fmt.Fprint(r.stderr, "\n")
			r.streamed = false
		}
		r.started[event.ToolCall.ID] = time.Now()
		summary := toolArgSummary(event.ToolCall.Arguments)
		line := r.paint("⏺", colorClaude) + " " + r.paint(toolDisplayName(event.ToolCall.Name), colorBold)
		if summary != "" {
			line += r.paint("("+summary+")", colorDim)
		}
		fmt.Fprintf(r.stderr, "  %s\n", line)
	}
	if (event.Type == transcript.ToolCallCompleted || event.Type == transcript.ToolCallFailed) && event.ToolCall != nil {
		r.stopWaitLocked()
		elapsed := time.Since(r.started[event.ToolCall.ID]).Round(10 * time.Millisecond)
		detail := toolResultSummary(event.ToolResult)
		if event.Type == transcript.ToolCallFailed {
			detail = "failed · " + elapsed.String()
			if event.ToolResult != nil {
				if msg := clipWidth(firstLine(event.ToolResult.Text()), 60); msg != "" {
					detail = msg + " · " + elapsed.String()
				}
			}
			fmt.Fprintf(r.stderr, "    %s %s\n", r.paint("✗", colorError), r.paint(detail, colorError))
		} else {
			if detail == "" {
				detail = elapsed.String()
			} else {
				detail = detail + " · " + elapsed.String()
			}
			fmt.Fprintf(r.stderr, "    %s %s\n", r.paint("⎿", colorDim), r.paint(detail, colorDim))
		}
		delete(r.started, event.ToolCall.ID)
		r.startWaitLocked()
	}
	if event.Type == transcript.TurnCompleted || event.Type == transcript.TurnFailed || event.Type == transcript.TurnCanceled {
		r.stopWaitLocked()
		r.streamed = false
		r.toolActivity = false
	}
}

func (r *eventRenderer) finish(result agentruntime.TurnResult) {
	if r.mode == "text" && result.Message.Text() != "" {
		fmt.Fprintln(r.stdout)
	}
}

func (r *eventRenderer) paint(value, code string) string {
	return paintANSI(r.color, value, code)
}

func toolDisplayName(name string) string {
	switch name {
	case "agent-explore":
		return "Explore"
	case "read_file":
		return "Read"
	case "write_file":
		return "Write"
	case "edit_file":
		return "Edit"
	case "list_files":
		return "List"
	case "skill_load":
		return "Skill"
	case "bash", "exec":
		return "Bash"
	case "grep":
		return "Grep"
	case "glob":
		return "Glob"
	}
	return name
}

func toolArgSummary(raw json.RawMessage) string {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return clipWidth(strings.Join(strings.Fields(string(raw)), " "), 56)
	}
	if tasks, ok := obj["tasks"].([]any); ok && len(tasks) > 0 {
		if first, ok := tasks[0].(map[string]any); ok {
			if task, _ := first["task"].(string); strings.TrimSpace(task) != "" {
				n := len(tasks)
				if n > 1 {
					return clipWidth(task, 44) + fmt.Sprintf(" · %d tasks", n)
				}
				return clipWidth(task, 56)
			}
		}
	}
	for _, key := range []string{"task", "command", "path", "file_path", "query", "glob", "pattern", "url", "name", "boundaries"} {
		if value, _ := obj[key].(string); strings.TrimSpace(value) != "" {
			return clipWidth(value, 56)
		}
	}
	if paths, ok := obj["paths"].([]any); ok && len(paths) > 0 {
		if path, _ := paths[0].(string); path != "" {
			if len(paths) > 1 {
				return clipWidth(path, 40) + fmt.Sprintf(" +%d", len(paths)-1)
			}
			return clipWidth(path, 56)
		}
	}
	return ""
}

func toolResultSummary(result *tool.Result) string {
	if result == nil {
		return ""
	}
	return clipWidth(firstLine(result.Text()), 56)
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if i := strings.IndexAny(value, "\n\r"); i >= 0 {
		return strings.TrimSpace(value[:i])
	}
	return value
}

package slackagent

import (
	"encoding/json"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/transcript"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
	"github.com/noknov/slack-copilot-agent/packages/slackconversation"
)

// Lifecycle projects canonical runtime events into Slack presentation state.
// It is deterministic and never invokes a model or creates conversation state.
func (s *slackStream) Lifecycle(event transcript.Event) {
	if s.status == nil {
		return
	}
	status, loading, ok := lifecycleStatus(event, slackconversation.IsCJK(s.req.Text))
	if !ok {
		return
	}
	s.mu.Lock()
	if s.lastStatus == status+"\x00"+loading {
		s.mu.Unlock()
		return
	}
	s.lastStatus = status + "\x00" + loading
	s.mu.Unlock()
	var messages []string
	if loading != "" {
		messages = []string{loading}
	}
	_ = s.status.SetThreadStatus(s.ctx, s.req.Channel, s.req.ThreadTS, status, messages)
}

func lifecycleStatus(event transcript.Event, cjk bool) (string, string, bool) {
	switch event.Type {
	case transcript.TurnStarted, transcript.ModelRequested:
		if cjk {
			return "正在思考", prompts.ToolStatus("thinking_zh", "思考中..."), true
		}
		return "is thinking", prompts.ToolStatus("thinking", "Thinking..."), true
	case transcript.ContextProjected:
		if cjk {
			return "正在处理", prompts.ToolStatus("context_zh", "正在整理上下文..."), true
		}
		return "is working", prompts.ToolStatus("context", "Reviewing context..."), true
	case transcript.CompactionCreated:
		if cjk {
			return "正在处理", prompts.ToolStatus("compacting_zh", "正在压缩会话上下文..."), true
		}
		return "is working", prompts.ToolStatus("compacting", "Compacting conversation context..."), true
	case transcript.ModelFailed:
		if !retryableModelFailure(event.Metadata) {
			return "", "", false
		}
		if cjk {
			return "正在重试", prompts.ToolStatus("retrying_zh", "模型暂时不可用，正在重试..."), true
		}
		return "is retrying", prompts.ToolStatus("retrying", "Model unavailable; retrying..."), true
	case transcript.ToolCallStarted:
		if event.ToolCall == nil || strings.TrimSpace(event.ToolCall.Name) == "" {
			return "", "", false
		}
		name := event.ToolCall.Name
		if cjk {
			return "正在处理", prompts.ToolStatus(name+"_zh", "正在使用 "+humanizeToolName(name)+"..."), true
		}
		return "is working", prompts.ToolStatus(name, "Using "+humanizeToolName(name)+"..."), true
	case transcript.ApprovalRequested:
		if cjk {
			return "正在等待", prompts.ToolStatus("approval_zh", "正在等待批准..."), true
		}
		return "is waiting", prompts.ToolStatus("approval", "Waiting for approval..."), true
	case transcript.TurnCompleted, transcript.TurnFailed, transcript.TurnCanceled:
		return "", "", true
	default:
		return "", "", false
	}
}

func retryableModelFailure(raw json.RawMessage) bool {
	var metadata struct {
		Retryable bool `json:"retryable"`
	}
	return json.Unmarshal(raw, &metadata) == nil && metadata.Retryable
}

func humanizeToolName(name string) string {
	name = strings.NewReplacer("-", " ", "_", " ").Replace(strings.TrimSpace(name))
	return strings.Join(strings.Fields(name), " ")
}

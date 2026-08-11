package slackagent

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
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
	s.setStatus(status, loading)
}

// StepSummary projects the primary model's narration from the same tool-call
// turn into Slack loading_messages. It replaces the v1 secondary-model call;
// the text is still a concrete summary of the current action and target.
func (s *slackStream) StepSummary(text string) {
	if s.status == nil {
		return
	}
	if s.sanitize != nil {
		text = s.sanitize(text)
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return
	}
	if utf8.RuneCountInString(text) > 120 {
		text = string([]rune(text)[:120]) + "…"
	}
	cjk := slackconversation.IsCJK(s.req.Text)
	status := "is working"
	if cjk {
		status = "正在处理"
	}
	s.setStatus(status, text)
}

func (s *slackStream) setStatus(status, loading string) {
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

const defaultStatusInstruction = `When an assistant turn calls one or more tools, include one short plain-language progress line in that same turn's text content. Describe the concrete action and target from the planned calls, such as "Reading the alert runbook" or "查询 payment 服务日志". Match the user's language, use at most 20 words or 20 Chinese characters, and output only that progress line before the structured tool calls. Do not mention tool names, function names, API paths, internal identifiers, or results you have not obtained. This line is transient Slack status, not part of the final answer.`

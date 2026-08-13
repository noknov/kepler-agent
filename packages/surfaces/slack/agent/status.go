package slackagent

import (
	"encoding/json"
	"strings"

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
	status, loading, ok := lifecycleStatus(event, slackconversation.IsChineseLocale(s.req.Locale))
	if !ok {
		return
	}
	s.setStatus(status, loading)
}

func (s *slackStream) setStatus(status, loading string) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.mu.Lock()
	key := statusKey(status, loading)
	if s.lastStatus == key || preservesProgressLoading(s.lastStatus, status, loading) {
		s.mu.Unlock()
		return
	}
	s.lastStatus = key
	s.statusEpoch++
	s.mu.Unlock()
	var messages []string
	if loading != "" {
		messages = []string{loading}
	}
	_ = s.status.SetThreadStatus(s.ctx, s.req.Channel, s.req.ThreadTS, status, messages)
}

func statusKey(status, loading string) string {
	return status + "\x00" + loading
}

func splitStatusKey(key string) (string, string) {
	status, loading, ok := strings.Cut(key, "\x00")
	if !ok {
		return key, ""
	}
	return status, loading
}

func preservesProgressLoading(currentKey, nextStatus, nextLoading string) bool {
	currentStatus, currentLoading := splitStatusKey(currentKey)
	if currentStatus != nextStatus || !isThinkingStatus(nextStatus) || !isDefaultThinkingLoading(nextLoading) {
		return false
	}
	return currentLoading != "" && !isDefaultThinkingLoading(currentLoading)
}

func isThinkingStatus(status string) bool {
	return status == "is thinking" || status == "正在思考"
}

func isDefaultThinkingLoading(loading string) bool {
	return loading == prompts.ToolStatus("thinking", "Thinking...") || loading == prompts.ToolStatus("thinking_zh", "思考中...")
}

func (s *slackStream) setProgressStatus(epoch uint64, status, loading string) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.mu.Lock()
	key := statusKey(status, loading)
	if s.statusEpoch != epoch || s.lastStatus == "\x00" || s.lastStatus == key {
		s.mu.Unlock()
		return
	}
	s.lastStatus = key
	s.mu.Unlock()
	_ = s.status.SetThreadStatus(s.ctx, s.req.Channel, s.req.ThreadTS, status, []string{loading})
}

func lifecycleStatus(event transcript.Event, cjk bool) (string, string, bool) {
	switch event.Type {
	case transcript.TurnStarted, transcript.ModelRequested:
		if cjk {
			return "正在思考", prompts.ToolStatus("thinking_zh", "思考中..."), true
		}
		return "is thinking", prompts.ToolStatus("thinking", "Thinking..."), true
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

package slackagent

import "github.com/noknov/slack-copilot-agent/packages/agent/model"

const toolProgressLabel = "Working"

func (s *slackStream) ToolStep(calls []model.ToolCall) {
	if s.status == nil || len(calls) == 0 {
		return
	}
	s.mu.Lock()
	if s.progressSeen == nil {
		s.progressSeen = make(map[string]bool)
	}
	for _, call := range calls {
		if call.Name == "" {
			continue
		}
		if call.ID != "" && s.progressSeen[call.ID] {
			continue
		}
		if call.ID != "" {
			s.progressSeen[call.ID] = true
		}
		s.statusEpoch++
		epoch := s.statusEpoch
		s.mu.Unlock()
		s.setProgressStatus(epoch, thinkingStatus(), toolProgressLabel)
		return
	}
	s.mu.Unlock()
}

func thinkingStatus() string { return "Thinking" }

func waitingStatus() string { return "Waiting" }

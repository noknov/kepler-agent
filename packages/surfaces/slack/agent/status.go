package slackagent

import (
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
)

// Lifecycle projects canonical runtime events into Slack presentation state.
// It is deterministic and never invokes a model or creates conversation state.
func (s *slackStream) Lifecycle(event transcript.Event) {
	if s.status == nil {
		return
	}
	status, ok := lifecycleStatus(event)
	if !ok {
		return
	}
	if status == "" {
		s.clearStatus()
		return
	}
	s.setLifecycleStatus(status)
}

// setLifecycleStatus changes only Slack's native phase label. The loading
// message belongs to the dynamic progress projector and must survive later
// model lifecycle events.
func (s *slackStream) setLifecycleStatus(status string) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.mu.Lock()
	currentStatus, currentLoading := splitStatusKey(s.lastStatus)
	if currentStatus == status {
		s.mu.Unlock()
		return
	}
	key := statusKey(status, currentLoading)
	s.lastStatus = key
	s.statusEpoch++
	s.mu.Unlock()
	var messages []string
	if currentLoading != "" {
		messages = []string{currentLoading}
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

// restoreThreadStatus re-applies dynamic loading after Slack clears thread status
// on reply delivery.
func (s *slackStream) restoreThreadStatus() {
	if s.status == nil {
		return
	}
	s.mu.Lock()
	if s.streamClosed || s.lastStatus == "" || s.lastStatus == "\x00" {
		s.mu.Unlock()
		return
	}
	status, loading := splitStatusKey(s.lastStatus)
	s.mu.Unlock()
	if loading == "" {
		return
	}
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	_ = s.status.SetThreadStatus(s.ctx, s.req.Channel, s.req.ThreadTS, status, []string{loading})
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

func lifecycleStatus(event transcript.Event) (string, bool) {
	switch event.Type {
	case transcript.ApprovalRequested:
		return "is waiting", true
	case transcript.TurnCompleted, transcript.TurnFailed, transcript.TurnCanceled:
		return "", true
	default:
		return "", false
	}
}

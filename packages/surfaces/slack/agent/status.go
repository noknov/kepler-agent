package slackagent

import (
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
)

const initialThreadStatus = "is thinking..."

// Lifecycle projects canonical runtime events into Slack presentation state.
// It is deterministic and never invokes a model or creates conversation state.
func (s *slackStream) Lifecycle(event transcript.Event) {
	if s.status == nil {
		return
	}
	switch event.Type {
	case transcript.TurnCompleted, transcript.TurnFailed, transcript.TurnCanceled:
		s.clearStatus()
	}
}

// startStatus starts Slack's native processing indicator as soon as the agent
// accepts a turn. Loading messages remain unset so Slack owns the initial UI;
// a later concrete tool operation may replace only that loading state.
func (s *slackStream) startStatus() {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.mu.Lock()
	if s.lastStatus != "" {
		s.mu.Unlock()
		return
	}
	s.lastStatus = statusKey(initialThreadStatus, "")
	s.statusEpoch++
	s.mu.Unlock()
	ctx, cancel := s.deliveryContext()
	defer cancel()
	_ = s.status.SetThreadStatus(ctx, s.req.Channel, s.req.ThreadTS, initialThreadStatus, nil)
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

func (s *slackStream) setProgressStatus(epoch uint64, loading string) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.mu.Lock()
	status, _ := splitStatusKey(s.lastStatus)
	if status == "" {
		status = initialThreadStatus
	}
	key := statusKey(status, loading)
	if s.statusEpoch != epoch || s.lastStatus == "\x00" || s.lastStatus == key {
		s.mu.Unlock()
		return
	}
	s.lastStatus = key
	s.mu.Unlock()
	ctx, cancel := s.deliveryContext()
	defer cancel()
	_ = s.status.SetThreadStatus(ctx, s.req.Channel, s.req.ThreadTS, status, []string{loading})
}

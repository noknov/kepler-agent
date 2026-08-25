package slackagent

import (
	"log"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
)

const (
	initialThreadStatus = "is thinking"
	typingThreadStatus  = "is typing"
	statusRefreshPeriod = 90 * time.Second
)

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
	s.sendThreadStatus(initialThreadStatus, nil, "start")
	s.armStatusRefresh()
}

// startTypingStatus replaces progress once the final assistant response starts
// streaming. A text delta is a canonical output event, so this never depends
// on model reasoning or prose classification.
func (s *slackStream) startTypingStatus() {
	if s.status == nil {
		return
	}
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.mu.Lock()
	if s.lastStatus == "\x00" || s.streamClosed {
		s.mu.Unlock()
		return
	}
	status, loading := splitStatusKey(s.lastStatus)
	if status == typingThreadStatus && loading == "" {
		s.mu.Unlock()
		return
	}
	s.lastStatus = statusKey(typingThreadStatus, "")
	s.statusEpoch++
	s.mu.Unlock()
	s.sendThreadStatus(typingThreadStatus, nil, "typing")
	s.armStatusRefresh()
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
	s.sendThreadStatus(status, []string{loading}, "restore")
	s.armStatusRefresh()
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
	s.sendThreadStatus(status, []string{loading}, "progress")
	s.armStatusRefresh()
}

func (s *slackStream) sendThreadStatus(status string, loading []string, source string) {
	ctx, cancel := s.deliveryContext()
	defer cancel()
	if err := s.status.SetThreadStatus(ctx, s.req.Channel, s.req.ThreadTS, status, loading); err != nil {
		log.Printf("slack thread status unavailable turn=%s source=%s", s.req.EventID, source)
	}
}

// Slack automatically removes a thread status after two minutes without a
// message. Refresh the current canonical status before that deadline while the
// turn remains active; this is independent of tool execution and model output.
func (s *slackStream) armStatusRefresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == nil || s.streamClosed || s.lastStatus == "" || s.lastStatus == "\x00" {
		return
	}
	if s.statusTimer != nil {
		s.statusTimer.Stop()
	}
	s.statusTimer = time.AfterFunc(statusRefreshPeriod, s.refreshStatus)
}

func (s *slackStream) stopStatusRefresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusTimer != nil {
		s.statusTimer.Stop()
		s.statusTimer = nil
	}
}

func (s *slackStream) refreshStatus() {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.mu.Lock()
	if s.status == nil || s.streamClosed || s.lastStatus == "" || s.lastStatus == "\x00" {
		s.statusTimer = nil
		s.mu.Unlock()
		return
	}
	status, loading := splitStatusKey(s.lastStatus)
	s.statusTimer = nil
	s.mu.Unlock()
	var loadingMessages []string
	if loading != "" {
		loadingMessages = []string{loading}
	}
	s.sendThreadStatus(status, loadingMessages, "refresh")
	s.armStatusRefresh()
}

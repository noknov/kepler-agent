package slackagent

import (
	"log"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

const (
	sessionProcessing = "processing"
	sessionActive     = "active"
	sessionSuspended  = "suspended"
)

// Lifecycle projects canonical turn completion into Slack's agent-session
// lifecycle. It does not infer progress from tools or issue model requests.
func (s *slackStream) Lifecycle(event transcript.Event) {
	if event.Type == transcript.PlanUpdated {
		s.UpdatePlan(event.Plan)
		return
	}
	if s.session == nil {
		return
	}
	switch event.Type {
	case transcript.TurnCompleted:
		s.setSessionStatus(sessionStatusForTermination(event.Status))
	case transcript.TurnFailed, transcript.TurnCanceled:
		s.setSessionStatus(sessionActive)
	}
}

func sessionStatusForTermination(termination string) string {
	switch termination {
	case "pending_input", "pending_approval":
		return sessionSuspended
	default:
		return sessionActive
	}
}

func (s *slackStream) setSessionStatus(status string) {
	if s.session == nil || status == "" {
		return
	}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.sessionStatus == status {
		return
	}
	ctx, cancel := s.deliveryContext()
	defer cancel()
	if err := s.session.SetAgentSessionStatus(ctx, s.req.Channel, s.req.ThreadTS, s.req.UserID, status); err != nil {
		log.Printf("slack agent session status unavailable turn=%s status=%s", s.req.EventID, status)
		return
	}
	s.sessionStatus = status
}

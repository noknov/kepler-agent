package slackbot

import (
	"context"

	"github.com/noknov/slack-copilot-agent/packages/slack"
)

func (s *Server) startEventWorkers(ctx context.Context) {
	if s.slackWorker != nil {
		s.slackWorker.Start(ctx)
	}
}

func (s *Server) enqueueSlackEvent(ctx context.Context, eventID string, event slack.Event) bool {
	if s.slackWorker == nil {
		return false
	}
	return s.slackWorker.Notify(ctx, eventID, event, s.eventEnqueueTimeout)
}

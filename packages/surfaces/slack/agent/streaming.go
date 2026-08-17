package slackagent

import (
	"strings"
	"time"

	slackconversation "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
)

const streamUpdateInterval = 400 * time.Millisecond

// AppendDelta buffers streamed assistant text and periodically updates the Slack message.
func (s *slackStream) AppendDelta(delta string) {
	if delta == "" {
		return
	}
	s.mu.Lock()
	s.answer.WriteString(delta)
	text := s.answer.String()
	s.mu.Unlock()
	s.scheduleStreamUpdate(text)
}

func (s *slackStream) scheduleStreamUpdate(text string) {
	s.mu.Lock()
	if s.streamClosed {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	if s.messageTS != "" && now.Sub(s.lastStreamUpdate) < streamUpdateInterval {
		if s.streamTimer == nil {
			s.streamTimer = time.AfterFunc(streamUpdateInterval, func() {
				s.mu.Lock()
				pending := s.answer.String()
				s.mu.Unlock()
				s.flushStreamUpdate(pending)
			})
		}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.flushStreamUpdate(text)
}

func (s *slackStream) flushStreamUpdate(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	streaming, ok := s.messenger.(slackconversation.StreamingMarkdownMessenger)
	if !ok {
		return
	}
	s.mu.Lock()
	if s.streamTimer != nil {
		s.streamTimer.Stop()
		s.streamTimer = nil
	}
	if s.messageTS == "" {
		ctx, cancel := s.deliveryContext()
		ts, err := streaming.PostMarkdownMessageWithID(ctx, s.req.Channel, s.req.ThreadTS, text, s.req.EventID)
		cancel()
		if err != nil {
			s.mu.Unlock()
			return
		}
		s.messageTS = ts
		s.streaming = true
		s.lastStreamUpdate = time.Now()
		s.mu.Unlock()
		return
	}
	messageTS := s.messageTS
	s.lastStreamUpdate = time.Now()
	s.mu.Unlock()
	ctx, cancel := s.deliveryContext()
	defer cancel()
	_ = streaming.UpdateMarkdownMessage(ctx, s.req.Channel, messageTS, text)
}

func (s *slackStream) stopStreamTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streamTimer != nil {
		s.streamTimer.Stop()
		s.streamTimer = nil
	}
}

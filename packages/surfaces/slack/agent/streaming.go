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
	s.beginAnswerStreaming()
	s.mu.Lock()
	s.answer.WriteString(delta)
	text := s.answer.String()
	s.mu.Unlock()
	s.scheduleStreamUpdate(text)
}

// beginAnswerStreaming retires tool-progress loading once assistant text starts
// streaming so later reply delivery does not resurrect stale operation labels.
func (s *slackStream) beginAnswerStreaming() {
	s.mu.Lock()
	defer s.mu.Unlock()
	status, loading := splitStatusKey(s.lastStatus)
	if loading == "" {
		return
	}
	s.lastStatus = statusKey(status, "")
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
				s.flushStreamUpdate(pending, false)
			})
		}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.flushStreamUpdate(text, false)
}

func (s *slackStream) shouldDeferStreamDelivery() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streamClosed {
		return true
	}
	return s.progressRunning || len(s.progressCalls) > 0
}

func (s *slackStream) flushDeferredStream(force bool) {
	s.mu.Lock()
	pending := s.answer.String()
	s.mu.Unlock()
	s.flushStreamUpdate(pending, force)
}

func (s *slackStream) flushStreamUpdate(text string, force bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if !force && s.shouldDeferStreamDelivery() {
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
		s.lastStreamText = text
		s.lastStreamUpdate = time.Now()
		s.mu.Unlock()
		s.restoreThreadStatus()
		return
	}
	if text == s.lastStreamText {
		s.mu.Unlock()
		return
	}
	messageTS := s.messageTS
	s.lastStreamText = text
	s.lastStreamUpdate = time.Now()
	s.mu.Unlock()
	ctx, cancel := s.deliveryContext()
	defer cancel()
	if err := streaming.UpdateMarkdownMessage(ctx, s.req.Channel, messageTS, text); err == nil {
		s.restoreThreadStatus()
	}
}

func (s *slackStream) stopStreamTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streamTimer != nil {
		s.streamTimer.Stop()
		s.streamTimer = nil
	}
}

func (s *slackStream) streamedText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastStreamText
}

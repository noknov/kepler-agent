package slackagent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	slackconversation "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
)

const (
	streamAppendInterval = 35 * time.Millisecond
	streamAppendMinChars = 32
)

// AppendDelta buffers streamed assistant text and periodically delivers it to Slack.
func (s *slackStream) AppendDelta(delta string) {
	if s.redactor != nil {
		delta = s.redactor.Append(delta)
	} else {
		delta = s.sanitizeText(delta)
	}
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
	pending := len(text) - len(s.lastStreamText)
	if s.messageTS != "" && !s.streamThrottleReady(now, pending) {
		if s.streamTimer == nil {
			s.streamTimer = time.AfterFunc(streamAppendInterval, func() {
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

func (s *slackStream) streamThrottleReady(now time.Time, pendingLen int) bool {
	if s.messageTS == "" {
		return true
	}
	return now.Sub(s.lastStreamUpdate) >= streamAppendInterval || pendingLen >= streamAppendMinChars
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
	s.flushNativeStream(text)
}

// ensureNativeStream opens the Slack native stream when the first assistant
// delta is ready. Tool progress remains a single temporary status until then,
// instead of creating a second, empty Slack message beside it.
func (s *slackStream) ensureNativeStream() {
	native, ok := s.messenger.(slackconversation.NativeStreamMessenger)
	if !ok {
		return
	}
	s.mu.Lock()
	if s.streamClosed || s.streamDeliveryFailed || s.messageTS != "" {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	ctx, cancel := s.deliveryContext()
	defer cancel()

	ts, err := native.StartStream(ctx, s.req.Channel, s.req.ThreadTS, s.req.UserID)
	if err != nil {
		log.Printf("slack native stream start failed channel=%s thread=%s user=%s: %v",
			s.req.Channel, s.req.ThreadTS, s.req.UserID, err)
		s.mu.Lock()
		s.streamDeliveryFailed = true
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.messageTS = ts
	s.nativeStream = true
	s.mu.Unlock()
}

func (s *slackStream) flushNativeStream(fullText string) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()

	s.mu.Lock()
	streamed := s.lastStreamText
	s.mu.Unlock()
	delta := streamSuffix(streamed, fullText)
	if delta == "" {
		return
	}
	s.mu.Lock()
	if s.streamTimer != nil {
		s.streamTimer.Stop()
		s.streamTimer = nil
	}
	deliveryFailed := s.streamDeliveryFailed
	s.mu.Unlock()
	if deliveryFailed {
		return
	}

	if err := s.appendNativeChunks(delta); err != nil {
		s.mu.Lock()
		s.streamDeliveryFailed = true
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.lastStreamText = fullText
	s.lastStreamUpdate = time.Now()
	s.mu.Unlock()
	s.restoreThreadStatus()
}

func (s *slackStream) sanitizeText(text string) string {
	return text
}

func (s *slackStream) appendNativeChunks(delta string) error {
	if delta == "" {
		return nil
	}
	native, ok := s.messenger.(slackconversation.NativeStreamMessenger)
	if !ok {
		return fmt.Errorf("native stream messenger unavailable")
	}
	s.ensureNativeStream()

	s.mu.Lock()
	messageTS := s.messageTS
	s.mu.Unlock()
	if messageTS == "" {
		return fmt.Errorf("native stream unavailable")
	}

	ctx, cancel := s.deliveryContext()
	defer cancel()

	chunks := []map[string]any{{"type": "markdown_text", "text": delta}}
	if err := native.AppendStream(ctx, s.req.Channel, messageTS, chunks); err == nil {
		return nil
	} else if !strings.Contains(err.Error(), "not_in_streaming_state") {
		log.Printf("slack native stream append failed channel=%s ts=%s: %v", s.req.Channel, messageTS, err)
		return err
	}

	ts, startErr := native.StartStream(ctx, s.req.Channel, s.req.ThreadTS, s.req.UserID)
	if startErr != nil {
		log.Printf("slack native stream restart failed channel=%s thread=%s: %v", s.req.Channel, s.req.ThreadTS, startErr)
		return startErr
	}
	s.mu.Lock()
	s.messageTS = ts
	s.nativeStream = true
	s.mu.Unlock()
	if err := native.AppendStream(ctx, s.req.Channel, ts, chunks); err != nil {
		log.Printf("slack native stream append after restart failed channel=%s ts=%s: %v", s.req.Channel, ts, err)
		return err
	}
	return nil
}

func streamSuffix(streamed, full string) string {
	full = strings.TrimSpace(full)
	if full == "" {
		return ""
	}
	streamed = strings.TrimSpace(streamed)
	if streamed == "" || !strings.HasPrefix(full, streamed) {
		return full
	}
	return full[len(streamed):]
}

func (s *slackStream) stopNativeStream(ctx context.Context) {
	if !s.nativeStream || s.messageTS == "" {
		return
	}
	native, ok := s.messenger.(slackconversation.NativeStreamMessenger)
	if !ok {
		return
	}
	_ = native.StopStream(ctx, s.req.Channel, s.messageTS)
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

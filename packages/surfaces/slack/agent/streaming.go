package slackagent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	"github.com/noknov/kepler-agent/packages/workflows"
)

const streamAppendInterval = 35 * time.Millisecond

// AppendDelta buffers streamed assistant text and periodically delivers it to Slack.
func (s *slackStream) AppendDelta(delta string) {
	s.mu.Lock()
	finalOnly := s.outputPolicy == workflows.OutputFinalOnly || s.streamClosing || s.streamClosed
	s.mu.Unlock()
	if finalOnly {
		return
	}
	if s.redactor != nil {
		delta = s.redactor.Append(delta)
	} else {
		delta = s.sanitizeText(delta)
	}
	if delta == "" {
		return
	}
	s.mu.Lock()
	s.answer.WriteString(delta)
	s.scheduleStreamUpdateLocked()
	s.mu.Unlock()
}

// At most one scheduled or in-flight delivery exists per stream. Deltas
// coalesce in answer while Slack is slow; the model callback never does I/O.
// Caller holds mu.
func (s *slackStream) scheduleStreamUpdateLocked() {
	if s.streamClosing || s.streamClosed || s.streamDeliveryFailed || s.streamTimer != nil || strings.TrimSpace(s.answer.String()) == s.lastStreamText {
		return
	}
	delay := max(time.Duration(0), streamAppendInterval-time.Since(s.lastStreamUpdate))
	s.streamTimer = time.AfterFunc(delay, func() {
		s.flushDeferredStream()
		s.mu.Lock()
		s.streamTimer = nil
		s.scheduleStreamUpdateLocked()
		s.mu.Unlock()
	})
}

// ensureNativeStream opens the Slack native stream when the first assistant
// delta is ready. Tool progress remains a single temporary status until then,
// instead of creating a second, empty Slack message beside it.
func (s *slackStream) ensureNativeStream(chunks []map[string]any) (bool, error) {
	native, ok := s.messenger.(slackconversation.NativeStreamMessenger)
	if !ok {
		return false, fmt.Errorf("native stream messenger unavailable")
	}
	s.mu.Lock()
	if s.streamClosed || s.streamDeliveryFailed || s.messageTS != "" {
		s.mu.Unlock()
		return false, nil
	}
	s.mu.Unlock()

	ctx, cancel := s.deliveryContext()
	defer cancel()

	start := slackconversation.StreamStart{
		Channel: s.req.Channel, ThreadTS: s.req.ThreadTS, RecipientUserID: s.req.UserID,
		Chunks: chunks,
	}
	if len(chunks) > 0 {
		start.TaskDisplayMode = "plan"
	}
	ts, err := native.StartStream(ctx, start)
	if err != nil {
		log.Printf("slack native stream start failed channel=%s thread=%s user=%s: %v",
			s.req.Channel, s.req.ThreadTS, s.req.UserID, err)
		s.mu.Lock()
		s.streamDeliveryFailed = true
		s.streamStartUncertain = isDeliveryUncertain(err)
		s.mu.Unlock()
		return false, err
	}
	s.mu.Lock()
	s.messageTS = ts
	s.nativeStream = true
	s.mu.Unlock()
	return true, nil
}

func (s *slackStream) flushDeferredStream() {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()

	s.mu.Lock()
	if s.streamClosed || s.streamDeliveryFailed {
		s.mu.Unlock()
		return
	}
	// Snapshot after acquiring the delivery lock; a queued callback must not
	// replay an older prefix after a later synchronous final flush.
	fullText := strings.TrimSpace(s.answer.String())
	streamed := s.lastStreamText
	s.mu.Unlock()
	delta := streamSuffix(streamed, fullText)
	if delta == "" {
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
	chunks := []map[string]any{{"type": "markdown_text", "text": delta}}
	started, err := s.ensureNativeStream(chunks)
	if err != nil {
		return err
	}
	if started {
		s.telemetry.answerDelivered()
		return nil
	}

	s.mu.Lock()
	messageTS := s.messageTS
	s.mu.Unlock()
	if messageTS == "" {
		return fmt.Errorf("native stream unavailable")
	}

	ctx, cancel := s.deliveryContext()
	defer cancel()

	if err := native.AppendStream(ctx, s.req.Channel, messageTS, chunks); err == nil {
		s.telemetry.answerDelivered()
		return nil
	} else if !isSlackError(err, "not_in_streaming_state") {
		log.Printf("slack native stream append failed channel=%s ts=%s: %v", s.req.Channel, messageTS, err)
		return err
	}

	// The original stream may already have been finalized by Slack. Starting a
	// replacement creates a second reply; let Complete repair the known message
	// with chat.update instead.
	log.Printf("slack native stream no longer active channel=%s ts=%s", s.req.Channel, messageTS)
	return fmt.Errorf("slack native stream is no longer active")
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

func isSlackError(err error, code string) bool {
	var slackErr slackconversation.SlackError
	return errors.As(err, &slackErr) && slackErr.SlackErrorCode() == code
}

func isDeliveryUncertain(err error) bool {
	var uncertain slackconversation.DeliveryUncertain
	return errors.As(err, &uncertain) && uncertain.DeliveryUncertain()
}

func (s *slackStream) stopNativeStream(ctx context.Context) {
	if !s.nativeStream || s.messageTS == "" {
		return
	}
	native, ok := s.messenger.(slackconversation.NativeStreamMessenger)
	if !ok {
		return
	}
	err := native.StopStream(ctx, s.req.Channel, s.messageTS)
	s.mu.Lock()
	s.streamStopError = err
	s.mu.Unlock()
}

func (s *slackStream) stopStreamTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamClosing = true
	if s.streamTimer != nil {
		s.streamTimer.Stop()
		s.streamTimer = nil
	}
}

func (s *slackStream) stopPlanTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.planTimer != nil {
		s.planTimer.Stop()
		s.planTimer = nil
	}
}

func (s *slackStream) streamedText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastStreamText
}

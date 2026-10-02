package slackagent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var replyTracer = otel.Tracer("github.com/noknov/kepler-agent/slack/reply")

type replyTrace struct {
	mu                                             sync.Mutex
	span                                           trace.Span
	started, messageAt, firstAnswerAt, deliveredAt time.Time
	status, errorCode                              string
	record                                         func(trace.Span, string, any)
}

func beginReplyTrace(ctx context.Context, sessionID string, req slackconversation.Request, record func(trace.Span, string, any)) (context.Context, *replyTrace) {
	ctx, span := replyTracer.Start(ctx, "slack.reply", trace.WithAttributes(
		attribute.String("langfuse.observation.type", "chain"),
		attribute.String("langfuse.trace.name", "kepler-agent.turn"),
		attribute.String("langfuse.session.id", sessionID),
		attribute.String("langfuse.user.id", req.UserID),
		attribute.String("langfuse.observation.metadata.turn_id", req.EventID),
		attribute.String("langfuse.trace.metadata.surface", "slack"),
		attribute.String("langfuse.observation.metadata.surface", "slack"),
	))
	t := &replyTrace{span: span, started: time.Now(), status: "not_attempted", record: record}
	// Slack's message timestamp survives inbox retries and queued turns. It
	// measures from the user's send time, rather than hiding queue delay.
	seconds, fraction, ok := strings.Cut(req.MessageTS, ".")
	sec, err := strconv.ParseInt(seconds, 10, 64)
	if ok && err == nil && len(fraction) <= 9 {
		ns, parseErr := strconv.ParseInt(fraction+strings.Repeat("0", 9-len(fraction)), 10, 64)
		if parseErr == nil {
			t.messageAt = time.Unix(sec, ns)
			if !t.messageAt.After(t.started) {
				span.SetAttributes(attribute.Int64("langfuse.observation.metadata.queue_wait_ms", t.started.Sub(t.messageAt).Milliseconds()), attribute.String("langfuse.observation.metadata.latency_origin", "slack_message_timestamp"))
			} else {
				t.messageAt = time.Time{}
			}
		}
	}
	if record != nil {
		record(span, "input", req.Message())
	}
	return ctx, t
}

// Only successful answer writes count as first response. Tool progress and
// agent-session status updates are intentionally excluded.
func (t *replyTrace) output(value string) {
	if t != nil && t.record != nil {
		t.record(t.span, "output", value)
	}
}

func (t *replyTrace) answerDelivered() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.firstAnswerAt.IsZero() {
		t.firstAnswerAt = time.Now()
	}
}

func (t *replyTrace) delivery(status string, err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status = status
	if status == "delivered" {
		t.deliveredAt = time.Now()
	}
	if err != nil {
		var coded slackconversation.SlackError
		if errors.As(err, &coded) {
			t.errorCode = coded.SlackErrorCode()
		} else {
			t.errorCode = "delivery_error"
		}
	}
}

func (t *replyTrace) end(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.span.End()
	if err != nil && t.status == "not_attempted" {
		t.status = "processing_failed"
	}
	t.span.SetAttributes(attribute.String("langfuse.observation.metadata.delivery_status", t.status), attribute.String("langfuse.observation.metadata.delivery_error_code", t.errorCode))
	if !t.firstAnswerAt.IsZero() {
		t.span.SetAttributes(attribute.Int64("langfuse.observation.metadata.first_reply_ms", t.firstAnswerAt.Sub(t.started).Milliseconds()))
		if !t.messageAt.IsZero() {
			t.span.SetAttributes(attribute.Int64("langfuse.observation.metadata.user_first_reply_ms", t.firstAnswerAt.Sub(t.messageAt).Milliseconds()))
		}
	}
	if t.status == "delivered" {
		t.span.SetAttributes(attribute.Bool("langfuse.observation.metadata.delivery_success", true))
		if !t.messageAt.IsZero() {
			t.span.SetAttributes(attribute.Int64("langfuse.observation.metadata.user_total_ms", t.deliveredAt.Sub(t.messageAt).Milliseconds()))
		}
	} else if t.status == "failed" || t.status == "processing_failed" {
		t.span.SetAttributes(attribute.Bool("langfuse.observation.metadata.delivery_success", false))
		t.span.SetStatus(codes.Error, t.status)
	} else if t.status == "uncertain" {
		t.span.SetAttributes(attribute.String("langfuse.observation.level", "WARNING"))
	}
}

func (s *slackStream) recordFinalDelivery(messageTS string, err error) {
	status := "delivered"
	s.mu.Lock()
	uncertain := s.streamStartUncertain || s.streamStopError != nil
	s.mu.Unlock()
	switch {
	case err != nil:
		status = "failed"
	case uncertain:
		status = "uncertain"
	case messageTS == "":
		status = "suppressed"
	}
	if err != nil && isDeliveryUncertain(err) {
		status = "uncertain"
	}
	s.telemetry.delivery(status, err)
	if status == "delivered" {
		s.telemetry.answerDelivered()
	}
}

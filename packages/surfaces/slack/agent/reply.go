package slackagent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/safety"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type slackStream struct {
	approvalNotifyUserID  string
	telemetry             *replyTrace
	streamStopError       error
	ctx                   context.Context
	messenger             slackconversation.Messenger
	req                   slackconversation.Request
	mu                    sync.Mutex
	deliveryMu            sync.Mutex
	sessionMu             sync.Mutex
	session               slackconversation.AgentSessionMessenger
	sessionStatus         string
	terminalSessionStatus string
	redactor              *safety.StreamRedactor
	answer                strings.Builder
	messageTS             string
	nativeStream          bool
	streamDeliveryFailed  bool
	streamStartUncertain  bool
	streamClosed          bool
	streamClosing         bool
	lastStreamText        string
	lastStreamUpdate      time.Time
	streamTimer           *time.Timer
	planDeliveryMu        sync.Mutex
	planClosing           bool
	planPostUncertain     bool
	pendingPlan           *tool.PlanUpdate
	planMessageTS         string
	planRevision          int
	planLastUpdate        time.Time
	planTimer             *time.Timer
	connectionActions     map[string]bool
	connections           *connections.Service
	finalizeOnce          sync.Once
	finalizeCtx           context.Context
	finalizeCancel        context.CancelFunc
	delivery              *replyDelivery
	reconcileStatus       func(context.Context, func(context.Context) error) error
}

func newSlackStream(ctx context.Context, messenger slackconversation.Messenger, req slackconversation.Request) *slackStream {
	return &slackStream{ctx: ctx, messenger: messenger, req: req}
}

func (s *slackStream) Start() {
	if session, ok := s.messenger.(slackconversation.AgentSessionMessenger); ok {
		s.session = session
		s.setSessionStatus(sessionProcessing)
	}
}

func (s *slackStream) Complete(final string) (string, error) {
	defer s.finishSession()
	s.telemetry.output(final)
	s.stopStreamTimer()
	s.finishPlan()
	if s.redactor != nil {
		s.appendSanitizedDelta(s.redactor.Flush())
	}
	s.flushDeferredStream()
	s.mu.Lock()
	s.streamClosed = true
	messageTS := s.messageTS
	nativeStream := s.nativeStream
	deliveryFailed := s.streamDeliveryFailed
	startUncertain := s.streamStartUncertain
	streamed := strings.TrimSpace(s.answer.String())
	s.mu.Unlock()
	ctx, cancel := s.deliveryContext()
	defer cancel()
	if final == "" {
		if nativeStream && messageTS != "" {
			s.stopNativeStream(ctx)
		}
		return messageTS, nil
	}
	if startUncertain && messageTS == "" {
		// The initial non-idempotent write may already exist in Slack. Posting a
		// fallback here would reintroduce the exact duplicate-message window.
		return "", nil
	}
	if nativeStream && messageTS != "" {
		trimmedFinal := strings.TrimSpace(final)
		if !strings.HasPrefix(trimmedFinal, streamed) {
			// The durable final answer supersedes intermediate commentary or
			// a revised response. Appending it would duplicate that content.
			deliveryFailed = true
		} else if suffix := streamSuffix(streamed, trimmedFinal); !deliveryFailed && suffix != "" && trimmedFinal != s.streamedText() {
			if err := s.appendNativeChunks(suffix); err != nil {
				deliveryFailed = true
			}
		}
		s.stopNativeStream(ctx)
		if deliveryFailed {
			return messageTS, s.updateKnownReply(ctx, messageTS, final)
		}
		return messageTS, nil
	}
	return s.postFinalMarkdown(ctx, final)
}

func (s *slackStream) appendSanitizedDelta(delta string) {
	if delta == "" {
		return
	}
	s.mu.Lock()
	s.answer.WriteString(delta)
	s.mu.Unlock()
}

func (s *slackStream) Fail(message string, canceled bool) (string, error) {
	defer s.setSessionStatus(sessionActive)
	s.stopStreamTimer()
	s.finishPlan()
	s.flushDeferredStream()
	s.mu.Lock()
	s.streamClosed = true
	messageTS := s.messageTS
	nativeStream := s.nativeStream
	startUncertain := s.streamStartUncertain
	s.mu.Unlock()
	if canceled {
		message = "Cancelled this request."
		if slackconversation.IsChineseLocale(s.req.Locale) {
			message = "已中止本次请求。"
		}
	}
	s.telemetry.output(message)
	if message == "" {
		ctx, cancel := s.deliveryContext()
		defer cancel()
		if nativeStream && messageTS != "" {
			s.stopNativeStream(ctx)
		}
		return messageTS, nil
	}
	if startUncertain && messageTS == "" {
		return "", nil
	}
	ctx, cancel := s.deliveryContext()
	defer cancel()
	if nativeStream && messageTS != "" {
		s.stopNativeStream(ctx)
		return messageTS, s.updateKnownReply(ctx, messageTS, message)
	}
	return s.postFinalMarkdown(ctx, message)
}

// A known reply can be repaired only in place. Creating another message would
// hide an unsupported update and duplicate the original answer.
func (s *slackStream) updateKnownReply(ctx context.Context, messageTS, text string) error {
	updater, ok := s.messenger.(slackconversation.MarkdownMessageUpdater)
	if !ok {
		return fmt.Errorf("Slack messenger cannot update existing reply %s", messageTS)
	}
	if err := updater.UpdateMarkdownMessage(ctx, s.req.Channel, messageTS, text); err != nil {
		return err
	}
	s.mu.Lock()
	s.streamStopError = nil
	s.mu.Unlock()
	return nil
}

func (s *slackStream) postFinalMarkdown(ctx context.Context, final string) (string, error) {
	return s.delivery.post(ctx, func() (string, error) {
		if messenger, ok := s.messenger.(slackconversation.IdempotentMarkdownMessenger); ok {
			return messenger.PostMarkdownMessageWithID(ctx, s.req.Channel, s.req.ThreadTS, final, s.req.EventID)
		}
		return s.messenger.PostMarkdownMessage(ctx, s.req.Channel, s.req.ThreadTS, final)
	})
}

func (s *slackStream) deliveryContext() (context.Context, context.CancelFunc) {
	base := s.ctx
	if base == nil {
		base = context.Background()
	}
	s.mu.Lock()
	closing := s.streamClosing
	s.mu.Unlock()
	if closing || base.Err() != nil {
		s.finalizeOnce.Do(func() {
			s.finalizeCtx, s.finalizeCancel = context.WithTimeout(context.WithoutCancel(base), 20*time.Second)
		})
		return context.WithCancel(s.finalizeCtx)
	}
	return context.WithTimeout(context.WithoutCancel(base), 20*time.Second)
}

func (s *slackStream) closeDeliveryContext() {
	if s.finalizeCancel != nil {
		s.finalizeCancel()
	}
}

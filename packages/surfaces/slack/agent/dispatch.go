package slackagent

import (
	"context"
	"log"
	"time"

	"github.com/noknov/kepler-agent/packages/session"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

// HandleMention admits authenticated input without occupying an execution slot.
// PostgreSQL acceptance precedes the Slack inbox acknowledgement. The bounded
// dispatcher and recovery poll consume the same durable input, including after
// a process exits between acceptance and its local wake-up.
func (s *Service) HandleMention(ctx context.Context, req conversation.Request) (bool, error) {
	if s.Messenger == nil || s.Agent.Runtime == nil {
		return false, nil
	}
	id := session.ID(req.Channel, req.ThreadTS)
	if controlled, err := s.controlActive(ctx, id, req); controlled || err != nil {
		return controlled, err
	}
	if err := s.persistInput(ctx, id, sessioninput.KindQueue, req); err != nil {
		return false, err
	}
	s.wakeExecution(id)
	return true, nil
}

// wakeExecution is a latency hint. Queue saturation never drops durable input.
func (s *Service) wakeExecution(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining || s.scheduled[id] || s.active[id] != nil {
		return
	}
	select {
	case s.dispatch <- id:
		s.scheduled[id] = true
	default:
	}
}

func (s *Service) executePending(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.dispatch:
			if ctx.Err() != nil {
				return
			}
			s.mu.Lock()
			if s.draining {
				delete(s.scheduled, id)
				s.mu.Unlock()
				return
			}
			if s.running == 0 {
				s.idle = make(chan struct{})
			}
			s.running++
			s.mu.Unlock()
			s.startPending(ctx, id)
			s.mu.Lock()
			delete(s.scheduled, id)
			s.running--
			if s.running == 0 {
				close(s.idle)
			}
			s.mu.Unlock()
			// A queued follow-up may have arrived while this session held its slot.
			s.recoverInputs(ctx)
		}
	}
}

func (s *Service) recoverInputs(ctx context.Context) {
	if s.Inputs == nil || ctx.Err() != nil {
		return
	}
	sessions, err := s.Inputs.PendingSessions(ctx, sessioninput.KindQueue, cap(s.dispatch))
	if err != nil {
		log.Printf("slack pending input recovery failed: %v", err)
		return
	}
	for _, id := range sessions {
		s.wakeExecution(id)
	}
}

const (
	inputClaimLease       = time.Minute
	inputRetryBaseDelay   = time.Second
	inputRetryMaxDelay    = time.Minute
	inputRetryMaxAttempts = 5
)

func (s *Service) failInput(ctx context.Context, id string, req conversation.Request, cause error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		_ = s.releaseClaim(cleanup, req.ClaimID)
		return
	}
	delay := inputRetryBaseDelay
	for attempt := 1; attempt < req.ClaimAttempt && delay < inputRetryMaxDelay; attempt++ {
		delay = min(inputRetryMaxDelay, delay*2)
	}
	dead, err := s.Inputs.Fail(cleanup, req.ClaimID, s.owner(), cause, delay, inputRetryMaxAttempts)
	if err != nil {
		log.Printf("slack failed input receipt error session=%s: %v", id, err)
		return
	}
	log.Printf("slack queued execution failed session=%s dead_letter=%t error_type=%T", id, dead, cause)
	if !dead {
		time.AfterFunc(delay, func() {
			if ctx.Err() == nil {
				s.wakeExecution(id)
			}
		})
	}
}

func (s *Service) Quiesce() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.draining = true
}

func (s *Service) WaitIdle(ctx context.Context) bool {
	s.mu.Lock()
	if s.running == 0 {
		s.mu.Unlock()
		return true
	}
	idle := s.idle
	s.mu.Unlock()
	select {
	case <-idle:
		return true
	case <-ctx.Done():
		return false
	}
}

package slackagent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type replyDelivery struct {
	mu      sync.Mutex
	store   DeliveryStore
	receipt ReplyReceipt
	err     error
}

func (s *Service) beginDelivery(ctx context.Context, sessionID string, req conversation.Request) (*replyDelivery, error) {
	if s.Deliveries == nil {
		return nil, nil
	}
	receipt, err := s.Deliveries.Begin(ctx, ReplyReceipt{
		TurnID: req.EventID, SessionID: sessionID, InputID: req.ClaimID,
		Owner: s.owner(), Attempt: req.ClaimAttempt,
		Channel: req.Channel, ThreadTS: req.ThreadTS, UserID: req.UserID,
	})
	if err != nil {
		return nil, err
	}
	d := &replyDelivery{store: s.Deliveries, receipt: receipt}
	// A previous process left intent without a receipt. Its request may have
	// succeeded. Never create another message based on the absence of a local ID.
	if receipt.Phase == replyStarting || receipt.Phase == replyPosting {
		if err := d.advance(ctx, replyUncertain, receipt.MessageTS); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func (d *replyDelivery) advance(ctx context.Context, phase, ts string) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.store.Advance(ctx, d.receipt, phase, ts); err != nil {
		d.err = err
		return err
	}
	d.receipt.Phase, d.receipt.MessageTS = phase, ts
	return nil
}

func (d *replyDelivery) post(ctx context.Context, send func() (string, error)) (string, error) {
	if d == nil {
		return send()
	}
	if err := d.advance(ctx, replyPosting, ""); err != nil {
		return "", err
	}
	ts, err := send()
	if err != nil {
		phase := replyPrepared
		if isDeliveryUncertain(err) {
			phase = replyUncertain
		}
		return ts, errors.Join(err, d.advance(ctx, phase, ts))
	}
	if ts == "" {
		return "", fmt.Errorf("Slack returned no delivery receipt")
	}
	return ts, d.advance(ctx, replyDelivered, ts)
}

func (s *Service) reconcileReplies(ctx context.Context) {
	if s.Deliveries == nil || s.Agent.Runtime == nil {
		return
	}
	records, err := s.Deliveries.Pending(ctx, 32)
	if err != nil {
		log.Printf("slack reply status recovery failed: %v", err)
		return
	}
	for _, receipt := range records {
		if ctx.Err() != nil {
			return
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		status := receipt.DesiredStatus
		settled, err := s.Agent.Runtime.ReconcileTurn(attemptCtx, receipt.SessionID, receipt.TurnID, func(ctx context.Context, termination agentruntime.TerminationReason) error {
			status = sessionStatusForTermination(string(termination))
			if err := s.Deliveries.Status(ctx, receipt, status, false); err != nil {
				return err
			}
			messenger, ok := s.Messenger.(conversation.AgentSessionMessenger)
			if !ok {
				return fmt.Errorf("Slack session messenger unavailable")
			}
			return messenger.SetAgentSessionStatus(ctx, receipt.Channel, receipt.ThreadTS, receipt.UserID, status)
		})
		if err == nil && settled {
			err = s.Deliveries.Status(attemptCtx, receipt, status, true)
		}
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Printf("slack reply status recovery failed turn=%s: %v", receipt.TurnID, err)
		}
	}
}

// This projection has a separate bounded lifetime: slow Slack status updates
// cannot occupy intake, execution or Stop dispatch.
func (s *Service) reconcileReplyLoop(ctx context.Context) {
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for {
		batch, cancel := context.WithTimeout(ctx, 10*time.Second)
		s.reconcileReplies(batch)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (d *replyDelivery) persistenceError() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

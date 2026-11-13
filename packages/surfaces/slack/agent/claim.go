package slackagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/noknov/kepler-agent/packages/sessioninput"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

// A short renewable input claim bounds restart recovery independently of the
// model deadline. Loss of ownership cancels the old executor before it can
// continue consuming input; the runtime's session lease still fences execution.
func (s *Service) keepInputClaim(ctx context.Context, sessionID, id string, cancelRun context.CancelFunc) func() {
	if id == "" || s.Inputs == nil {
		return func() {}
	}
	ctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(inputClaimLease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.Inputs.Renew(ctx, id, s.owner(), inputClaimLease); err != nil {
					if ctx.Err() == nil {
						log.Printf("slack input claim renewal failed: %v", err)
						cancelRun()
					}
					return
				}
				if s.Redis != nil {
					hintCtx, cancel := context.WithTimeout(ctx, time.Second)
					_ = s.Redis.Set(hintCtx, "agent:active:"+sessionID, s.PodID, inputClaimLease)
					cancel()
				}
			}
		}
	}()
	return func() { stop(); <-done }
}

func (s *Service) owner() string {
	if s.PodID != "" {
		return s.PodID
	}
	return "local-slack-service"
}

type claimedInput struct {
	slackconversation.Request
}

func (s *Service) claimNextQueue(ctx context.Context, sessionID string) (*claimedInput, error) {
	if s.Inputs == nil {
		return nil, nil
	}
	items, err := s.Inputs.Claim(ctx, sessionID, sessioninput.KindQueue, s.owner(), inputClaimLease, 1)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	var request slackconversation.Request
	decodeErr := json.Unmarshal(items[0].Payload, &request)
	request.ClaimID = items[0].ID
	request.ClaimAttempt = items[0].Attempts
	claimed := &claimedInput{Request: request}
	if decodeErr != nil {
		return claimed, fmt.Errorf("decode queued input: %w", decodeErr)
	}
	if request.EventID != items[0].ID || request.UserID == "" {
		return claimed, fmt.Errorf("queued input identity does not match its receipt")
	}
	return claimed, nil
}

func (s *Service) ackClaim(ctx context.Context, id string) error {
	if id == "" || s.Inputs == nil {
		return nil
	}
	return s.Inputs.Ack(ctx, id, s.owner())
}

func (s *Service) releaseClaim(ctx context.Context, id string) error {
	if id == "" || s.Inputs == nil {
		return nil
	}
	return s.Inputs.Release(ctx, id, s.owner())
}

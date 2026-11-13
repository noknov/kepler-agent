package slackagent

import (
	"context"
	"log"
	"time"
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

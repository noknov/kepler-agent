package slackagent

import (
	"context"
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/sessioninput"
	"github.com/redis/go-redis/v9"
)

func (s *Service) StartControlSubscriber(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	var executions sync.WaitGroup
	workers := s.MaxConcurrentRuns
	if workers <= 0 {
		workers = 8
	}
	stopHints := make(chan string, workers)
	for range workers {
		executions.Add(1)
		go func() { defer executions.Done(); s.executePending(ctx) }()
		executions.Add(1)
		go func() {
			defer executions.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-stopHints:
					s.handleStopSignal(ctx, id)
				}
			}
		}()
	}
	defer func() { cancel(); executions.Wait() }()
	executions.Add(1)
	go func() { defer executions.Done(); s.reconcileReplyLoop(ctx) }()
	s.recoverInputs(ctx)

	if (s.Redis == nil || s.PodID == "") && s.Inputs == nil {
		return
	}
	var messages <-chan *redis.Message
	var closeSub func()
	if s.Redis != nil && s.PodID != "" {
		sub := s.Redis.Subscribe(ctx, "agent:control:"+s.PodID, sessionStopChannel)
		messages = sub.Channel()
		closeSub = func() { _ = sub.Close() }
		defer closeSub()
	}
	approvalMessages := (<-chan *redis.Message)(nil)
	var closeApproval func()
	if s.Redis != nil {
		sub := s.Redis.Subscribe(ctx, "agent:approval")
		approvalMessages = sub.Channel()
		closeApproval = func() { _ = sub.Close() }
		defer closeApproval()
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-messages:
			if !ok {
				messages = nil
				continue
			}
			if message.Channel == sessionStopChannel {
				wakeStop(stopHints, message.Payload)
			} else {
				s.wakeExecution(message.Payload)
			}
		case message, ok := <-approvalMessages:
			if !ok {
				approvalMessages = nil
				continue
			}
			s.wakeExecution(message.Payload)
		case <-ticker.C:
			if s.Inputs == nil {
				continue
			}
			s.pollSessionStops(stopHints)
			_, _ = s.Inputs.PromoteExpiredSteering(ctx, 2*inputClaimLease)
			sessions, err := s.Inputs.PendingSessions(ctx, sessioninput.KindQueue, 100)
			if err != nil {
				continue
			}
			for _, sessionID := range sessions {
				s.wakeExecution(sessionID)
			}
		}
	}
}

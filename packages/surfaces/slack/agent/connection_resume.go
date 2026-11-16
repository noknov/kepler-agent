package slackagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/redis/go-redis/v9"

	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

// RecoverConnections derives readiness from durable authorization facts. Pub/Sub
// reduces latency; startup and periodic recovery do not depend on a notification.
func (s *Service) RecoverConnections(ctx context.Context) {
	if s.Continuations == nil || s.Agent.Runtime == nil {
		return
	}
	var wake <-chan *redis.Message
	if s.Redis != nil {
		sub := s.Redis.Subscribe(ctx, connections.OAuthCompletedChannel)
		defer sub.Close()
		wake = sub.Channel()
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		s.recoverConnections(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case _, ok := <-wake:
			if !ok {
				wake = nil
			}
		}
	}
}
func (s *Service) recoverConnections(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	pending, err := s.Continuations.ClaimReady(claimCtx, continuationBatchSize)
	cancel()
	if err != nil {
		log.Printf("connection continuation recovery failed: %v", err)
		return
	}
	for _, c := range pending {
		s.resumeAfterConnection(ctx, c)
	}
}

func (s *Service) resumeAfterConnection(parentCtx context.Context, continuation ConnectionContinuation) {
	ctx, cancel := context.WithTimeout(parentCtx, 10*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		s.releaseContinuation(ctx, continuation)
		return
	}
	target := slackconversation.Target{Kind: continuation.SessionKind, Channel: continuation.Channel, ThreadTS: continuation.ThreadTS}
	sessionID := target.SessionID()
	if target.Validate() != nil || continuation.UserID == "" || continuation.SessionID != sessionID || continuation.TurnID == "" || continuation.TeamID == "" {
		if s.Continuations != nil {
			if err := s.Continuations.Clear(ctx, continuation); err != nil {
				log.Printf("connection continuation cleanup failed session=%s: %v", sessionID, err)
				s.releaseContinuation(ctx, continuation)
			}
		}
		return
	}
	// The original turn may still be completing when OAuth finishes. Persist
	// the conditional input now; the runtime checks it after acquiring the session.
	identity, _ := json.Marshal([]string{sessionID, continuation.TurnID, continuation.UserID, continuation.Provider, continuation.InstanceID})
	id := fmt.Sprintf("connection-continue-%x", sha256.Sum256(identity))
	req := slackconversation.Request{
		TeamID:           continuation.TeamID,
		SessionKind:      continuation.SessionKind,
		ConversationType: continuation.ConversationType,
		EventID:          id,
		MessageID:        id,
		ResumeFromTurnID: continuation.TurnID,
		UserID:           continuation.UserID,
		Channel:          continuation.Channel,
		ThreadTS:         continuation.ThreadTS,
		Text:             "I've connected. Please continue.",
	}
	accepted, err := s.HandleMention(ctx, req)
	if err != nil {
		log.Printf("connection continuation resume failed session=%s: %v", sessionID, err)
		s.releaseContinuation(ctx, continuation)
		return
	}
	if !accepted {
		log.Printf("connection continuation resume not accepted session=%s", sessionID)
		s.releaseContinuation(ctx, continuation)
		return
	}
	if s.Continuations != nil {
		if err := s.Continuations.Clear(ctx, continuation); err != nil {
			log.Printf("connection continuation cleanup failed session=%s: %v", sessionID, err)
			s.releaseContinuation(ctx, continuation)
		}
	}
}

func (s *Service) releaseContinuation(ctx context.Context, continuation ConnectionContinuation) {
	if s.Continuations != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.Continuations.Release(ctx, continuation)
	}
}

package slackagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/noknov/kepler-agent/packages/connections"

	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

func (s *Service) StartConnectionCompletedSubscriber(ctx context.Context) {
	if s.Redis == nil || s.Continuations == nil || s.Agent.Runtime == nil {
		return
	}
	sub := s.Redis.Subscribe(ctx, connections.OAuthCompletedChannel)
	defer func() { _ = sub.Close() }()
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-sub.Channel():
			if !ok {
				return
			}
			userID, provider, instanceID, parsed := connections.ParseOAuthCompletedPayload(message.Payload)
			if !parsed {
				continue
			}
			continuations, err := s.Continuations.Claim(ctx, userID, provider, instanceID)
			if err != nil {
				log.Printf("connection continuation claim failed user=%s provider=%s: %v", userID, provider, err)
				continue
			}
			for _, continuation := range continuations {
				// Resumption only admits durable input; model work stays in the
				// bounded dispatcher. Do not detach a worker per saved thread.
				s.resumeAfterConnection(ctx, continuation)
			}
		}
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
			_ = s.Continuations.Clear(ctx, continuation)
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
		_ = s.Continuations.Clear(ctx, continuation)
	}
}

func (s *Service) releaseContinuation(ctx context.Context, continuation ConnectionContinuation) {
	if s.Continuations != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.Continuations.Release(ctx, continuation)
	}
}

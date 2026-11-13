package slackagent

import (
	"context"
	"encoding/json"
	"fmt"

	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type steeringInput struct {
	userID    string
	store     sessioninput.Store
	sessionID string
	owner     string
}

func (s *steeringInput) Push(ctx context.Context, request slackconversation.Request) error {
	if s.store == nil {
		return fmt.Errorf("durable session input store is unavailable")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return s.store.Enqueue(ctx, sessioninput.Item{ID: request.EventID, SessionID: s.sessionID, Kind: sessioninput.KindSteering, Payload: payload})
}

func (s *steeringInput) Claim(ctx context.Context, limit int) ([]agentruntime.PendingInput, error) {
	if s.store == nil {
		return nil, nil
	}
	items, err := s.store.Claim(ctx, s.sessionID, sessioninput.KindSteering, s.owner, inputClaimLease, limit)
	if err != nil {
		return nil, err
	}
	inputs := make([]agentruntime.PendingInput, 0, len(items))
	for _, item := range items {
		var request slackconversation.Request
		if err := json.Unmarshal(item.Payload, &request); err != nil {
			return nil, err
		}
		if s.userID == "" || request.UserID != s.userID {
			if err := s.store.Requeue(ctx, item.ID, s.owner); err != nil {
				return nil, err
			}
			continue
		}
		inputs = append(inputs, agentruntime.PendingInput{ID: item.ID, Message: request.Message()})
	}
	return inputs, nil
}

func (s *steeringInput) Ack(ctx context.Context, id string) error {
	if s.store == nil {
		return fmt.Errorf("durable session input store is unavailable")
	}
	return s.store.Ack(ctx, id, s.owner)
}

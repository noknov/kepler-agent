package slackagent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/infra/redisclient"
	"github.com/redis/go-redis/v9"
)

const (
	continuationTTL      = 24 * time.Hour
	continuationClaimTTL = 5 * time.Minute
)

// ConnectionContinuation captures a Slack thread that paused for OAuth.
type ConnectionContinuation struct {
	UserID           string `json:"user_id"`
	Provider         string `json:"provider"`
	InstanceID       string `json:"instance_id,omitempty"`
	SessionID        string `json:"session_id"`
	TurnID           string `json:"turn_id,omitempty"`
	TeamID           string `json:"team_id,omitempty"`
	ConversationType string `json:"conversation_type,omitempty"`
	Channel          string `json:"channel"`
	ThreadTS         string `json:"thread_ts"`

	claimOwner string
	snapshot   string
}

// ConnectionContinuationStore owns pending Slack threads and their claim-scoped cleanup.
type ConnectionContinuationStore interface {
	Save(ctx context.Context, continuation ConnectionContinuation) error
	Claim(ctx context.Context, userID, provider, instanceID string) ([]ConnectionContinuation, error)
	Release(ctx context.Context, continuation ConnectionContinuation) error
	Clear(ctx context.Context, continuation ConnectionContinuation) error
}

type RedisContinuationStore struct {
	Redis *redisclient.Client
}

// RuntimeConnectionContinuationStore adapts the shared runtime contract to Slack's
// OAuth continuation implementation.
type RuntimeConnectionContinuationStore struct{ Store ConnectionContinuationStore }

func (s RuntimeConnectionContinuationStore) Save(ctx context.Context, continuation agentruntime.ConnectionContinuation) error {
	if s.Store == nil {
		return nil
	}
	return s.Store.Save(ctx, ConnectionContinuation{
		UserID: continuation.Scope.UserID, Provider: continuation.Provider, InstanceID: continuation.InstanceID, SessionID: continuation.Scope.SessionID,
		TurnID: continuation.TurnID,
		TeamID: continuation.Scope.Values["team_id"], ConversationType: continuation.Scope.Values["conversation_type"],
		Channel: continuation.Scope.Values["channel"], ThreadTS: continuation.Scope.Values["thread_ts"],
	})
}

func NewRedisContinuationStore(rdb *redisclient.Client) *RedisContinuationStore {
	if rdb == nil {
		return nil
	}
	return &RedisContinuationStore{Redis: rdb}
}

func continuationIndexKey(userID, provider, instanceID string) string {
	return "connection:pending:" + userID + ":" + provider + ":" + continuationInstanceID(instanceID)
}

func continuationKey(continuation ConnectionContinuation) string {
	return continuationIndexKey(continuation.UserID, continuation.Provider, continuation.InstanceID) + ":" + continuation.SessionID
}

func continuationClaimKey(continuation ConnectionContinuation) string {
	return continuationKey(continuation) + ":claim"
}

func (s *RedisContinuationStore) Save(ctx context.Context, continuation ConnectionContinuation) error {
	if s == nil || s.Redis == nil {
		return nil
	}
	if continuation.UserID == "" || continuation.Provider == "" || continuation.Channel == "" || continuation.SessionID == "" || continuation.TurnID == "" {
		return fmt.Errorf("continuation requires an actor, provider, channel, session and waiting turn")
	}
	continuation.InstanceID = continuationInstanceID(continuation.InstanceID)
	payload, err := json.Marshal(continuation)
	if err != nil {
		return err
	}
	_, err = s.Redis.Eval(ctx, `
redis.call('SET',KEYS[1],ARGV[1],'EX',ARGV[3])
redis.call('SADD',KEYS[2],ARGV[2])
redis.call('EXPIRE',KEYS[2],ARGV[3])
redis.call('DEL',KEYS[3])
return 1`, []string{continuationKey(continuation), continuationIndexKey(continuation.UserID, continuation.Provider, continuation.InstanceID), continuationClaimKey(continuation)}, string(payload), continuation.SessionID, int64(continuationTTL/time.Second))
	return err
}

func (s *RedisContinuationStore) Claim(ctx context.Context, userID, provider, instanceID string) ([]ConnectionContinuation, error) {
	if s == nil || s.Redis == nil || userID == "" || provider == "" {
		return nil, nil
	}
	sessions, err := s.Redis.SMembers(ctx, continuationIndexKey(userID, provider, instanceID))
	if err != nil {
		return nil, err
	}
	continuations := make([]ConnectionContinuation, 0, len(sessions))
	complete := false
	defer func() {
		if complete {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		for _, claimed := range continuations {
			_ = s.Release(cleanup, claimed)
		}
	}()
	for _, sessionID := range sessions {
		candidate := ConnectionContinuation{UserID: userID, Provider: provider, InstanceID: instanceID, SessionID: sessionID}
		owner := rand.Text()
		value, err := s.Redis.Eval(ctx, `
local value=redis.call('GET',KEYS[1])
if not value then redis.call('SREM',KEYS[2],ARGV[2]); return nil end
if redis.call('SET',KEYS[3],ARGV[1],'NX','EX',ARGV[3]) then return value end
return nil`, []string{continuationKey(candidate), continuationIndexKey(userID, provider, instanceID), continuationClaimKey(candidate)}, owner, sessionID, int64(continuationClaimTTL/time.Second))
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		raw, ok := value.(string)
		if !ok {
			_ = s.Redis.DeleteIfValue(ctx, continuationClaimKey(candidate), owner)
			return nil, fmt.Errorf("invalid continuation storage response")
		}
		var continuation ConnectionContinuation
		if err := json.Unmarshal([]byte(raw), &continuation); err != nil {
			_ = s.Redis.DeleteIfValue(ctx, continuationClaimKey(candidate), owner)
			return nil, err
		}
		continuation.claimOwner, continuation.snapshot = owner, raw
		continuations = append(continuations, continuation)
	}
	complete = true
	return continuations, nil
}

func (s *RedisContinuationStore) Clear(ctx context.Context, continuation ConnectionContinuation) error {
	if s == nil || s.Redis == nil || continuation.UserID == "" || continuation.Provider == "" || continuation.SessionID == "" {
		return nil
	}
	if continuation.claimOwner == "" || continuation.snapshot == "" {
		return fmt.Errorf("clearing a continuation requires its claim")
	}
	_, err := s.Redis.Eval(ctx, `
if redis.call('GET',KEYS[3])~=ARGV[1] then return 0 end
if redis.call('GET',KEYS[1])==ARGV[2] then
  redis.call('DEL',KEYS[1]); redis.call('SREM',KEYS[2],ARGV[3])
end
redis.call('DEL',KEYS[3])
return 1`, []string{continuationKey(continuation), continuationIndexKey(continuation.UserID, continuation.Provider, continuation.InstanceID), continuationClaimKey(continuation)}, continuation.claimOwner, continuation.snapshot, continuation.SessionID)
	return err
}

// Release makes a claimed continuation available for another delivery attempt.
// It intentionally preserves both the payload and index membership.
func (s *RedisContinuationStore) Release(ctx context.Context, continuation ConnectionContinuation) error {
	if s == nil || s.Redis == nil || continuation.UserID == "" || continuation.Provider == "" || continuation.SessionID == "" {
		return nil
	}
	if continuation.claimOwner == "" {
		return fmt.Errorf("releasing a continuation requires its claim")
	}
	return s.Redis.DeleteIfValue(ctx, continuationClaimKey(continuation), continuation.claimOwner)
}

func continuationInstanceID(instanceID string) string {
	if value := strings.TrimSpace(instanceID); value != "" {
		return value
	}
	return connections.DefaultInstanceID
}

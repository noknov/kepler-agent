package connections

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/infra/redisclient"
	"github.com/redis/go-redis/v9"
)

const OAuthCompletedChannel = "connection:oauth:completed"

const (
	continuationTTL      = 24 * time.Hour
	continuationClaimTTL = 5 * time.Minute
)

// Continuation captures a Slack thread that paused for OAuth.
type Continuation struct {
	UserID    string `json:"user_id"`
	Provider  string `json:"provider"`
	SessionID string `json:"session_id"`
	Channel   string `json:"channel"`
	ThreadTS  string `json:"thread_ts"`
}

// ContinuationStore persists pending OAuth continuations and notifies workers.
type ContinuationStore interface {
	Save(ctx context.Context, continuation Continuation) error
	Claim(ctx context.Context, userID, provider string) ([]Continuation, error)
	Release(ctx context.Context, continuation Continuation) error
	Clear(ctx context.Context, continuation Continuation) error
	PublishCompleted(ctx context.Context, userID, provider string) error
}

type RedisContinuationStore struct {
	Redis *redisclient.Client
}

// RuntimeContinuationStore adapts the shared runtime contract to Slack's
// OAuth continuation implementation.
type RuntimeContinuationStore struct{ Store ContinuationStore }

func (s RuntimeContinuationStore) Save(ctx context.Context, continuation agentruntime.ConnectionContinuation) error {
	if s.Store == nil {
		return nil
	}
	return s.Store.Save(ctx, Continuation{
		UserID: continuation.UserID, Provider: continuation.Provider, SessionID: continuation.SessionID,
		Channel: continuation.Channel, ThreadTS: continuation.ThreadTS,
	})
}

func NewRedisContinuationStore(rdb *redisclient.Client) *RedisContinuationStore {
	if rdb == nil {
		return nil
	}
	return &RedisContinuationStore{Redis: rdb}
}

func continuationIndexKey(userID, provider string) string {
	return "connection:pending:" + userID + ":" + provider
}

func continuationKey(continuation Continuation) string {
	return continuationIndexKey(continuation.UserID, continuation.Provider) + ":" + continuation.SessionID
}

func continuationClaimKey(continuation Continuation) string {
	return continuationKey(continuation) + ":claim"
}

func (s *RedisContinuationStore) Save(ctx context.Context, continuation Continuation) error {
	if s == nil || s.Redis == nil {
		return nil
	}
	if continuation.UserID == "" || continuation.Provider == "" || continuation.Channel == "" {
		return nil
	}
	payload, err := json.Marshal(continuation)
	if err != nil {
		return err
	}
	if err := s.Redis.Set(ctx, continuationKey(continuation), string(payload), continuationTTL); err != nil {
		return err
	}
	if _, err := s.Redis.SAdd(ctx, continuationIndexKey(continuation.UserID, continuation.Provider), continuation.SessionID); err != nil {
		return err
	}
	return s.Redis.Expire(ctx, continuationIndexKey(continuation.UserID, continuation.Provider), continuationTTL)
}

func (s *RedisContinuationStore) Claim(ctx context.Context, userID, provider string) ([]Continuation, error) {
	if s == nil || s.Redis == nil || userID == "" || provider == "" {
		return nil, nil
	}
	sessions, err := s.Redis.SMembers(ctx, continuationIndexKey(userID, provider))
	if err != nil {
		return nil, err
	}
	continuations := make([]Continuation, 0, len(sessions))
	for _, sessionID := range sessions {
		candidate := Continuation{UserID: userID, Provider: provider, SessionID: sessionID}
		claimed, err := s.Redis.SetNX(ctx, continuationClaimKey(candidate), "1", continuationClaimTTL)
		if err != nil || !claimed {
			continue
		}
		raw, err := s.Redis.Get(ctx, continuationKey(candidate))
		if errors.Is(err, redis.Nil) || strings.TrimSpace(raw) == "" {
			_ = s.Redis.Del(ctx, continuationClaimKey(candidate))
			_, _ = s.Redis.SRem(ctx, continuationIndexKey(userID, provider), sessionID)
			continue
		}
		if err != nil {
			_ = s.Redis.Del(ctx, continuationClaimKey(candidate))
			return nil, err
		}
		var continuation Continuation
		if err := json.Unmarshal([]byte(raw), &continuation); err != nil {
			_ = s.Redis.Del(ctx, continuationClaimKey(candidate))
			return nil, err
		}
		continuations = append(continuations, continuation)
	}
	return continuations, nil
}

func (s *RedisContinuationStore) Clear(ctx context.Context, continuation Continuation) error {
	if s == nil || s.Redis == nil || continuation.UserID == "" || continuation.Provider == "" || continuation.SessionID == "" {
		return nil
	}
	if err := s.Redis.Del(ctx, continuationKey(continuation), continuationClaimKey(continuation)); err != nil {
		return err
	}
	_, err := s.Redis.SRem(ctx, continuationIndexKey(continuation.UserID, continuation.Provider), continuation.SessionID)
	return err
}

// Release makes a claimed continuation available for another delivery attempt.
// It intentionally preserves both the payload and index membership.
func (s *RedisContinuationStore) Release(ctx context.Context, continuation Continuation) error {
	if s == nil || s.Redis == nil || continuation.UserID == "" || continuation.Provider == "" || continuation.SessionID == "" {
		return nil
	}
	return s.Redis.Del(ctx, continuationClaimKey(continuation))
}

func (s *RedisContinuationStore) PublishCompleted(ctx context.Context, userID, provider string) error {
	if s == nil || s.Redis == nil || userID == "" || provider == "" {
		return nil
	}
	return s.Redis.Publish(ctx, OAuthCompletedChannel, oauthCompletedPayload(userID, provider))
}

func oauthCompletedPayload(userID, provider string) string {
	return userID + "|" + provider
}

func ParseOAuthCompletedPayload(payload string) (userID, provider string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(payload), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (s Service) notifyOAuthCompleted(ctx context.Context, userID, provider string) {
	if s.OnOAuthCompleted != nil {
		_ = s.OnOAuthCompleted(ctx, userID, provider)
	}
	if s.Continuations != nil {
		_ = s.Continuations.PublishCompleted(ctx, userID, provider)
	}
}

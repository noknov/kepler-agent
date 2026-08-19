package connections

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/redis/go-redis/v9"
)

const OAuthCompletedChannel = "connection:oauth:completed"

const continuationTTL = 24 * time.Hour

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
	Load(ctx context.Context, userID, provider string) (Continuation, bool, error)
	Clear(ctx context.Context, userID, provider string) error
	PublishCompleted(ctx context.Context, userID, provider string) error
}

type RedisContinuationStore struct {
	Redis *redisclient.Client
}

func NewRedisContinuationStore(rdb *redisclient.Client) *RedisContinuationStore {
	if rdb == nil {
		return nil
	}
	return &RedisContinuationStore{Redis: rdb}
}

func continuationKey(userID, provider string) string {
	return "connection:pending:" + userID + ":" + provider
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
	return s.Redis.Set(ctx, continuationKey(continuation.UserID, continuation.Provider), string(payload), continuationTTL)
}

func (s *RedisContinuationStore) Load(ctx context.Context, userID, provider string) (Continuation, bool, error) {
	if s == nil || s.Redis == nil || userID == "" || provider == "" {
		return Continuation{}, false, nil
	}
	raw, err := s.Redis.Get(ctx, continuationKey(userID, provider))
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return Continuation{}, false, nil
		}
		return Continuation{}, false, err
	}
	if strings.TrimSpace(raw) == "" {
		return Continuation{}, false, nil
	}
	var continuation Continuation
	if err := json.Unmarshal([]byte(raw), &continuation); err != nil {
		return Continuation{}, false, err
	}
	return continuation, true, nil
}

func (s *RedisContinuationStore) Clear(ctx context.Context, userID, provider string) error {
	if s == nil || s.Redis == nil || userID == "" || provider == "" {
		return nil
	}
	return s.Redis.Del(ctx, continuationKey(userID, provider))
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

package connections

import (
	"context"
	"strings"

	"github.com/noknov/kepler-agent/packages/infra/redisclient"
)

const OAuthCompletedChannel = "connection:oauth:completed"

// OAuthCompletionNotifier publishes credential changes without owning a
// surface's waiting conversations or execution lifecycle.
type OAuthCompletionNotifier interface {
	PublishCompleted(context.Context, string, string, string) error
}

type RedisOAuthCompletionNotifier struct{ Redis *redisclient.Client }

func (s RedisOAuthCompletionNotifier) PublishCompleted(ctx context.Context, userID, provider, instanceID string) error {
	if s.Redis == nil || userID == "" || provider == "" {
		return nil
	}
	return s.Redis.Publish(ctx, OAuthCompletedChannel, oauthCompletedPayload(userID, provider, instanceID))
}

func oauthCompletedPayload(userID, provider, instanceID string) string {
	return userID + "|" + provider + "|" + connectionInstanceID(instanceID)
}

func ParseOAuthCompletedPayload(payload string) (userID, provider, instanceID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(payload), "|")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	if len(parts) == 3 {
		instanceID = connectionInstanceID(parts[2])
	}
	return parts[0], parts[1], instanceID, true
}

func (s Service) notifyOAuthCompleted(ctx context.Context, userID, provider, instanceID string) {
	s.notifyConnectionChanged(ctx, userID, provider)
	if s.OAuthCompleted != nil {
		_ = s.OAuthCompleted.PublishCompleted(ctx, userID, provider, instanceID)
	}
}

// Connection invalidation refreshes presentation, never resumes a waiting turn.
func (s Service) notifyConnectionChanged(ctx context.Context, userID, provider string) {
	if s.OnConnectionChanged != nil {
		_ = s.OnConnectionChanged(ctx, userID, provider)
	}
}

package connections

import (
	"context"
	"time"
)

type Store interface {
	Get(ctx context.Context, userID, provider string) (Connection, error)
	List(ctx context.Context, userID string) ([]Connection, error)
	UpsertToken(ctx context.Context, userID, provider, token string, scopes []string, account string) error
	Delete(ctx context.Context, userID, provider string) error
	Token(ctx context.Context, userID, provider string) (string, error)

	CreateOAuthState(ctx context.Context, userID, provider, state string, expiresAt time.Time) error
	PeekOAuthState(ctx context.Context, state string) (userID, provider string, err error)
	ConsumeOAuthState(ctx context.Context, state string) (userID, provider string, err error)
}

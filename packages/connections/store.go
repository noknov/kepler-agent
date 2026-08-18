package connections

import (
	"context"
	"time"
)

// OAuthStateMeta carries provider-specific OAuth state.
type OAuthStateMeta struct {
	CodeVerifier string
}

type Store interface {
	Get(ctx context.Context, userID, provider string) (Connection, error)
	List(ctx context.Context, userID string) ([]Connection, error)
	UpsertToken(ctx context.Context, userID, provider, token string, scopes []string, account string) error
	Delete(ctx context.Context, userID, provider string) error
	Token(ctx context.Context, userID, provider string) (string, error)
	RawToken(ctx context.Context, userID, provider string) (string, error)
	AnyToken(ctx context.Context, provider string) (string, error)
	AnyTokenUser(ctx context.Context, provider string) (userID, rawToken string, err error)

	CreateOAuthState(ctx context.Context, userID, provider, state string, expiresAt time.Time, meta OAuthStateMeta) error
	PeekOAuthState(ctx context.Context, state string) (userID, provider string, meta OAuthStateMeta, err error)
	ConsumeOAuthState(ctx context.Context, state string) (userID, provider string, meta OAuthStateMeta, err error)
}

package connections

import (
	"context"
	"time"
)

// OAuthStateMeta carries provider-specific OAuth state.
type OAuthStateMeta struct {
	InstanceID   string
	Label        string
	Metadata     map[string]string
	CodeVerifier string
	// Origin and ReturnContext are explicit surface handoff data. The
	// connections package treats them as opaque strings; surfaces decide how
	// to interpret the return context.
	Origin        string
	ReturnContext string
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

// InstanceStore is the extensible connection contract. Store remains
// provider-only for compatibility with existing adapters; services use this
// interface when a backend supports multiple instances of one provider.
type InstanceStore interface {
	GetInstance(ctx context.Context, userID, provider, instanceID string) (Connection, error)
	ListInstances(ctx context.Context, userID string) ([]Connection, error)
	UpsertTokenInstance(ctx context.Context, userID, provider, instanceID, label, token string, scopes []string, account string, metadata map[string]string) error
	DeleteInstance(ctx context.Context, userID, provider, instanceID string) error
	TokenInstance(ctx context.Context, userID, provider, instanceID string) (string, error)
	RawTokenInstance(ctx context.Context, userID, provider, instanceID string) (string, error)
}

func connectionInstanceID(instanceID string) string {
	instanceID = NormalizeInstanceID(instanceID)
	if instanceID == "" {
		return DefaultInstanceID
	}
	return instanceID
}

func instanceStore(store Store) (InstanceStore, bool) {
	item, ok := store.(InstanceStore)
	return item, ok && item != nil
}

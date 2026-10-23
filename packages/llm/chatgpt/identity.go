package chatgpt

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

type identityKey struct{}

// BindIdentity pins a request to the authenticated account and registration.
// Token refresh preserves this identity; signing in as another account does
// not. The transport verifies it again under its credential-file lock.
func BindIdentity(ctx context.Context, path string) (context.Context, string, error) {
	session, err := Load(path)
	if err != nil {
		return ctx, "", err
	}
	identity := sessionIdentity(session)
	return context.WithValue(ctx, identityKey{}, identity), identity, nil
}

func sessionIdentity(session Session) string {
	raw, _ := json.Marshal([]string{session.Subject, session.ClientID, session.HostID})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

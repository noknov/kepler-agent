package connections

import (
	"context"
	"fmt"
)

func (s *Service) loadCredential(ctx context.Context, userID, provider, instanceID string) (Connection, string, error) {
	store, ok := s.Store.(CredentialStore)
	if !ok {
		return Connection{}, "", fmt.Errorf("connection store does not support versioned credentials")
	}
	return store.LoadCredential(ctx, userID, provider, connectionInstanceID(instanceID))
}

func (s *Service) updateCredential(ctx context.Context, conn Connection, token string) error {
	store, ok := s.Store.(CredentialStore)
	if !ok {
		return fmt.Errorf("connection store does not support conditional credential updates")
	}
	return store.UpdateCredential(ctx, conn, token)
}

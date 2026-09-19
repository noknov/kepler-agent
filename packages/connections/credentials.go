package connections

import (
	"context"
	"strings"
)

// ClearProvider deletes stored credentials for a user integration. Use when the
// remote service rejects the bearer token so later access checks report disconnected.
func (s *Service) ClearProvider(ctx context.Context, userID, provider string) error {
	return s.ClearProviderInstance(ctx, userID, provider, DefaultInstanceID)
}

// ClearProviderInstance removes exactly one selected connection. It is used
// by per-instance MCP resolvers so a rejected server credential cannot clear a
// sibling instance of the same provider.
func (s *Service) ClearProviderInstance(ctx context.Context, userID, provider, instanceID string) error {
	if s == nil || s.Store == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	provider = strings.TrimSpace(provider)
	if userID == "" || provider == "" {
		return nil
	}
	if err := s.deleteConnection(ctx, userID, provider, instanceID); err != nil {
		return err
	}
	s.notifyConnectionChanged(ctx, userID, provider)
	return nil
}

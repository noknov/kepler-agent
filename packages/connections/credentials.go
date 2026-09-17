package connections

import (
	"context"
	"strings"
)

// ClearProvider deletes stored credentials for a user integration. Use when the
// remote service rejects the bearer token so later access checks report disconnected.
func (s *Service) ClearProvider(ctx context.Context, userID, provider string) error {
	if s == nil || s.Store == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	provider = strings.TrimSpace(provider)
	if userID == "" || provider == "" {
		return nil
	}
	if err := s.Store.Delete(ctx, userID, provider); err != nil {
		return err
	}
	s.notifyConnectionChanged(ctx, userID, provider)
	return nil
}

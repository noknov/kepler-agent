package connections

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// GCPAccessToken returns a valid GCP API access token for the user, refreshing when needed.
func (s *Service) GCPAccessToken(ctx context.Context, userID string) (string, error) {
	return s.GCPAccessTokenInstance(ctx, userID, DefaultInstanceID)
}

// GCPAccessTokenInstance resolves one explicitly selected Google account. The
// provider-only method remains the legacy default-instance alias.
func (s *Service) GCPAccessTokenInstance(ctx context.Context, userID, instanceID string) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", ErrNotConnected
	}
	bundle, conn, err := s.loadGCPBundleInstance(ctx, userID, instanceID)
	if err != nil {
		return "", err
	}
	refreshed, err := s.ensureFreshGCPBundleInstance(ctx, userID, bundle, conn)
	if err != nil {
		return "", err
	}
	return refreshed.Access, nil
}

func (s *Service) loadGCPBundle(ctx context.Context, userID string) (clickStackTokenBundle, Connection, error) {
	return s.loadGCPBundleInstance(ctx, userID, DefaultInstanceID)
}

func (s *Service) loadGCPBundleInstance(ctx context.Context, userID, instanceID string) (clickStackTokenBundle, Connection, error) {
	conn, err := s.GetConnection(ctx, userID, ProviderGCP, instanceID)
	if err != nil {
		return clickStackTokenBundle{}, Connection{}, err
	}
	raw, err := s.gcpRawTokenInstance(ctx, userID, conn.InstanceID)
	if err != nil {
		return clickStackTokenBundle{}, Connection{}, err
	}
	bundle, err := parseClickStackTokenBundle(raw)
	if err != nil {
		return clickStackTokenBundle{}, Connection{}, fmt.Errorf("parse gcp token for %s: %w", userID, err)
	}
	return bundle, conn, nil
}

func (s *Service) ensureFreshGCPBundle(ctx context.Context, userID string, bundle clickStackTokenBundle, conn Connection) (clickStackTokenBundle, error) {
	return s.ensureFreshGCPBundleInstance(ctx, userID, bundle, conn)
}

func (s *Service) ensureFreshGCPBundleInstance(ctx context.Context, userID string, bundle clickStackTokenBundle, conn Connection) (clickStackTokenBundle, error) {
	now := time.Now().UTC()
	if !bundle.needsRefresh(now) {
		return bundle, nil
	}
	mu := s.gcpRefreshMutex(userID + "\x00" + connectionInstanceID(conn.InstanceID))
	mu.Lock()
	defer mu.Unlock()

	if latest, latestConn, err := s.loadGCPBundleInstance(ctx, userID, conn.InstanceID); err == nil {
		bundle = latest
		conn = latestConn
		if !bundle.needsRefresh(time.Now().UTC()) {
			return bundle, nil
		}
	}

	if strings.TrimSpace(bundle.Refresh) == "" {
		return clickStackTokenBundle{}, s.RequiredInstance(userID, ProviderGCP, conn.InstanceID)
	}
	if !s.Config.GCP.Enabled() {
		return clickStackTokenBundle{}, fmt.Errorf("gcp oauth is not configured")
	}

	response, err := s.gcp().refresh(ctx, bundle.Refresh)
	if err != nil {
		return clickStackTokenBundle{}, fmt.Errorf("refresh gcp token: %w", err)
	}

	refresh := response.RefreshToken
	if refresh == "" {
		refresh = bundle.Refresh
	}
	updated := clickStackTokenBundle{
		Access:    response.AccessToken,
		Refresh:   refresh,
		ExpiresAt: gcpExpiresAt(response.AccessToken, response.ExpiresIn),
	}
	stored, err := encodeClickStackTokenBundle(updated)
	if err != nil {
		return clickStackTokenBundle{}, err
	}
	account := conn.Account
	if label := s.gcp().accountLabel(ctx, response.AccessToken); label != "" {
		account = label
	}
	scopes := conn.Scopes
	if parsed := gcpScopes(response); len(parsed) > 0 {
		scopes = parsed
	}
	if err := s.upsertToken(ctx, userID, ProviderGCP, conn.InstanceID, conn.Label, stored, scopes, account, conn.Metadata); err != nil {
		return clickStackTokenBundle{}, err
	}
	return updated, nil
}

func (s *Service) gcpRawTokenInstance(ctx context.Context, userID, instanceID string) (string, error) {
	if store, ok := instanceStore(s.Store); ok {
		return store.RawTokenInstance(ctx, userID, ProviderGCP, instanceID)
	}
	if connectionInstanceID(instanceID) != DefaultInstanceID {
		return "", ErrNotConnected
	}
	return s.Store.RawToken(ctx, userID, ProviderGCP)
}

func (s *Service) gcpRefreshMutex(userID string) *sync.Mutex {
	value, _ := s.mutableState().gcpRefresh.LoadOrStore(userID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s *Service) storeGCPBundle(ctx context.Context, userID string, response gcpTokenResponse, account string, scopes []string) error {
	return s.storeGCPBundleInstance(ctx, userID, DefaultInstanceID, "", nil, response, account, scopes)
}

func (s *Service) storeGCPBundleInstance(ctx context.Context, userID, instanceID, label string, metadata map[string]string, response gcpTokenResponse, account string, scopes []string) error {
	bundle := clickStackTokenBundle{
		Access:    response.AccessToken,
		Refresh:   response.RefreshToken,
		ExpiresAt: gcpExpiresAt(response.AccessToken, response.ExpiresIn),
	}
	stored, err := encodeClickStackTokenBundle(bundle)
	if err != nil {
		return err
	}
	if label := s.gcp().accountLabel(ctx, response.AccessToken); label != "" {
		account = label
	}
	if len(scopes) == 0 {
		scopes = gcpScopes(response)
	}
	return s.upsertToken(ctx, userID, ProviderGCP, instanceID, label, stored, scopes, account, metadata)
}

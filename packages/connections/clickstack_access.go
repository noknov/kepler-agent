package connections

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ClickStackAccessToken returns a valid ClickStack MCP access token for the user,
// refreshing it with the stored OAuth refresh token when needed.
func (s *Service) ClickStackAccessToken(ctx context.Context, userID string) (string, error) {
	return s.ClickStackAccessTokenInstance(ctx, userID, DefaultInstanceID)
}

// ClickStackAccessTokenInstance resolves and refreshes one selected server
// instance. The provider-only method above remains the default-instance alias.
func (s *Service) ClickStackAccessTokenInstance(ctx context.Context, userID, instanceID string) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", ErrNotConnected
	}
	bundle, conn, err := s.loadClickStackBundleInstance(ctx, userID, instanceID)
	if err != nil {
		return "", err
	}
	s.maybeBackfillClickStackAccount(ctx, userID, bundle, conn)
	refreshed, err := s.ensureFreshClickStackBundleInstance(ctx, userID, bundle, conn)
	if err != nil {
		return "", err
	}
	return refreshed.Access, nil
}

// ClickStackConnected reports whether the user has a usable ClickStack token stored.
func (s *Service) ClickStackConnected(ctx context.Context, userID string) bool {
	return s.ClickStackConnectedInstance(ctx, userID, DefaultInstanceID)
}

func (s *Service) ClickStackConnectedInstance(ctx context.Context, userID, instanceID string) bool {
	if s.Store == nil || strings.TrimSpace(userID) == "" {
		return false
	}
	raw, err := s.rawTokenInstance(ctx, userID, ProviderClickStack, instanceID)
	if err != nil {
		return false
	}
	return clickStackStoredTokenUsable(raw, s.Config.ClickStack.OAuthMode())
}

func clickStackStoredTokenUsable(raw string, oauthMode bool) bool {
	bundle, err := parseClickStackTokenBundle(raw)
	if err != nil || strings.TrimSpace(bundle.Access) == "" {
		return false
	}
	if oauthMode {
		return strings.TrimSpace(bundle.Refresh) != ""
	}
	return true
}

func (s *Service) loadClickStackBundle(ctx context.Context, userID string) (clickStackTokenBundle, Connection, error) {
	return s.loadClickStackBundleInstance(ctx, userID, DefaultInstanceID)
}

func (s *Service) loadClickStackBundleInstance(ctx context.Context, userID, instanceID string) (clickStackTokenBundle, Connection, error) {
	conn, err := s.GetConnection(ctx, userID, ProviderClickStack, instanceID)
	if err != nil {
		return clickStackTokenBundle{}, Connection{}, err
	}
	raw, err := s.rawTokenInstance(ctx, userID, ProviderClickStack, instanceID)
	if err != nil {
		return clickStackTokenBundle{}, Connection{}, err
	}
	bundle, err := parseClickStackTokenBundle(raw)
	if err != nil {
		return clickStackTokenBundle{}, Connection{}, fmt.Errorf("parse clickstack token for %s: %w", userID, err)
	}
	return bundle, conn, nil
}

func (s *Service) ensureFreshClickStackBundle(ctx context.Context, userID string, bundle clickStackTokenBundle, conn Connection) (clickStackTokenBundle, error) {
	return s.ensureFreshClickStackBundleInstance(ctx, userID, bundle, conn)
}

func (s *Service) ensureFreshClickStackBundleInstance(ctx context.Context, userID string, bundle clickStackTokenBundle, conn Connection) (clickStackTokenBundle, error) {
	now := time.Now().UTC()
	if !bundle.needsRefresh(now) {
		return bundle, nil
	}
	mu := s.clickstackRefreshMutex(userID + "\x00" + connectionInstanceID(conn.InstanceID))
	mu.Lock()
	defer mu.Unlock()

	if latest, latestConn, err := s.loadClickStackBundleInstance(ctx, userID, conn.InstanceID); err == nil {
		bundle = latest
		conn = latestConn
		if !bundle.needsRefresh(time.Now().UTC()) {
			return bundle, nil
		}
	}

	if strings.TrimSpace(bundle.Refresh) == "" {
		return clickStackTokenBundle{}, s.RequiredInstance(userID, ProviderClickStack, conn.InstanceID)
	}
	if !s.Config.ClickStackEnabled() && strings.TrimSpace(conn.Metadata["mcp_url"]) == "" {
		return clickStackTokenBundle{}, fmt.Errorf("clickstack oauth is not configured")
	}

	redirectURI := bundle.RedirectURI
	if redirectURI == "" {
		redirectURI = s.callbackURL(ProviderClickStack)
	}
	clientID := bundle.ClientID
	if clientID == "" {
		var err error
		clientID, err = s.clickstackForConnection(conn).ensureClient(ctx, redirectURI)
		if err != nil {
			return clickStackTokenBundle{}, err
		}
	}

	response, err := s.clickstackForConnection(conn).refresh(ctx, bundle.Refresh, redirectURI, clientID)
	if err != nil {
		return clickStackTokenBundle{}, fmt.Errorf("refresh clickstack token: %w", err)
	}

	refresh := response.RefreshToken
	if refresh == "" {
		refresh = bundle.Refresh
	}
	updated := clickStackTokenBundle{
		Access:      response.AccessToken,
		Refresh:     refresh,
		ClientID:    clientID,
		RedirectURI: redirectURI,
		ExpiresAt:   clickStackExpiresAt(response.AccessToken, response.ExpiresIn),
	}
	stored, err := encodeClickStackTokenBundle(updated)
	if err != nil {
		return clickStackTokenBundle{}, err
	}
	account := conn.Account
	if label := s.clickstackForConnection(conn).accountLabel(response.AccessToken, response.IDToken); label != "" {
		account = label
	}
	scopes := conn.Scopes
	if len(response.Scopes) > 0 {
		scopes = response.Scopes
	}
	if err := s.upsertToken(ctx, userID, ProviderClickStack, conn.InstanceID, conn.Label, stored, scopes, account, conn.Metadata); err != nil {
		return clickStackTokenBundle{}, err
	}
	return updated, nil
}

func (s *Service) maybeBackfillClickStackAccount(ctx context.Context, userID string, bundle clickStackTokenBundle, conn Connection) {
	if strings.TrimSpace(conn.Account) != "" {
		return
	}
	label := s.clickstackForConnection(conn).accountLabel(bundle.Access, "")
	if label == "" {
		return
	}
	stored, err := encodeClickStackTokenBundle(bundle)
	if err != nil {
		return
	}
	_ = s.upsertToken(ctx, userID, ProviderClickStack, conn.InstanceID, conn.Label, stored, conn.Scopes, label, conn.Metadata)
}

func (s *Service) clickstackRefreshMutex(userID string) *sync.Mutex {
	value, _ := s.mutableState().clickstackRefresh.LoadOrStore(userID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s *Service) clickstackForConnection(conn Connection) *clickstackOAuth {
	meta := OAuthStateMeta{Metadata: conn.Metadata}
	return s.clickstackForMeta(meta)
}

func (s *Service) rawTokenInstance(ctx context.Context, userID, provider, instanceID string) (string, error) {
	if store, ok := instanceStore(s.Store); ok {
		return store.RawTokenInstance(ctx, userID, provider, instanceID)
	}
	if connectionInstanceID(instanceID) != DefaultInstanceID {
		return "", ErrNotConnected
	}
	return s.Store.RawToken(ctx, userID, provider)
}

func (s *Service) storeClickStackBundle(ctx context.Context, userID, clientID, redirectURI string, response clickstackTokenResponse, account string, scopes []string) error {
	return s.storeClickStackBundleInstance(ctx, userID, DefaultInstanceID, "", nil, clientID, redirectURI, response, account, scopes)
}

func (s *Service) storeClickStackBundleInstance(ctx context.Context, userID, instanceID, label string, metadata map[string]string, clientID, redirectURI string, response clickstackTokenResponse, account string, scopes []string) error {
	bundle := clickStackTokenBundle{
		Access:      response.AccessToken,
		Refresh:     response.RefreshToken,
		ClientID:    clientID,
		RedirectURI: redirectURI,
		ExpiresAt:   clickStackExpiresAt(response.AccessToken, response.ExpiresIn),
	}
	stored, err := encodeClickStackTokenBundle(bundle)
	if err != nil {
		return err
	}
	if label := s.clickstack().accountLabel(response.AccessToken, response.IDToken); label != "" {
		account = label
	}
	return s.upsertToken(ctx, userID, ProviderClickStack, instanceID, label, stored, scopes, account, metadata)
}

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
	if strings.TrimSpace(userID) == "" {
		return "", ErrNotConnected
	}
	bundle, conn, err := s.loadClickStackBundle(ctx, userID)
	if err != nil {
		return "", err
	}
	s.maybeBackfillClickStackAccount(ctx, userID, bundle, conn)
	refreshed, err := s.ensureFreshClickStackBundle(ctx, userID, bundle, conn)
	if err != nil {
		return "", err
	}
	return refreshed.Access, nil
}

// ClickStackConnected reports whether the user has a usable ClickStack token stored.
func (s *Service) ClickStackConnected(ctx context.Context, userID string) bool {
	if s.Store == nil || strings.TrimSpace(userID) == "" {
		return false
	}
	raw, err := s.Store.RawToken(ctx, userID, ProviderClickStack)
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
	conn, err := s.Store.Get(ctx, userID, ProviderClickStack)
	if err != nil {
		return clickStackTokenBundle{}, Connection{}, err
	}
	raw, err := s.Store.RawToken(ctx, userID, ProviderClickStack)
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
	now := time.Now().UTC()
	if !bundle.needsRefresh(now) {
		return bundle, nil
	}
	mu := s.clickstackRefreshMutex(userID)
	mu.Lock()
	defer mu.Unlock()

	if latest, latestConn, err := s.loadClickStackBundle(ctx, userID); err == nil {
		bundle = latest
		conn = latestConn
		if !bundle.needsRefresh(time.Now().UTC()) {
			return bundle, nil
		}
	}

	if strings.TrimSpace(bundle.Refresh) == "" {
		return clickStackTokenBundle{}, s.Required(userID, ProviderClickStack)
	}
	if !s.Config.ClickStackEnabled() {
		return clickStackTokenBundle{}, fmt.Errorf("clickstack oauth is not configured")
	}

	redirectURI := bundle.RedirectURI
	if redirectURI == "" {
		redirectURI = s.callbackURL(ProviderClickStack)
	}
	clientID := bundle.ClientID
	if clientID == "" {
		var err error
		clientID, err = s.clickstack().ensureClient(ctx, redirectURI)
		if err != nil {
			return clickStackTokenBundle{}, err
		}
	}

	response, err := s.clickstack().refresh(ctx, bundle.Refresh, redirectURI, clientID)
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
	if label := s.clickstack().accountLabel(response.AccessToken, response.IDToken); label != "" {
		account = label
	}
	scopes := conn.Scopes
	if len(response.Scopes) > 0 {
		scopes = response.Scopes
	}
	if err := s.Store.UpsertToken(ctx, userID, ProviderClickStack, stored, scopes, account); err != nil {
		return clickStackTokenBundle{}, err
	}
	return updated, nil
}

func (s *Service) maybeBackfillClickStackAccount(ctx context.Context, userID string, bundle clickStackTokenBundle, conn Connection) {
	if strings.TrimSpace(conn.Account) != "" {
		return
	}
	label := s.clickstack().accountLabel(bundle.Access, "")
	if label == "" {
		return
	}
	stored, err := encodeClickStackTokenBundle(bundle)
	if err != nil {
		return
	}
	_ = s.Store.UpsertToken(ctx, userID, ProviderClickStack, stored, conn.Scopes, label)
}

func (s *Service) clickstackRefreshMutex(userID string) *sync.Mutex {
	value, _ := s.mutableState().clickstackRefresh.LoadOrStore(userID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s *Service) storeClickStackBundle(ctx context.Context, userID, clientID, redirectURI string, response clickstackTokenResponse, account string, scopes []string) error {
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
	return s.Store.UpsertToken(ctx, userID, ProviderClickStack, stored, scopes, account)
}

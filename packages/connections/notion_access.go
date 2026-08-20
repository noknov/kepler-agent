package connections

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// NotionAccessToken returns a valid Notion MCP access token for the user,
// refreshing it with the stored OAuth refresh token when needed.
func (s *Service) NotionAccessToken(ctx context.Context, userID string) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", ErrNotConnected
	}
	bundle, conn, err := s.loadNotionBundle(ctx, userID)
	if err != nil {
		return "", err
	}
	s.maybeBackfillNotionAccount(ctx, userID, bundle, conn)
	refreshed, err := s.ensureFreshNotionBundle(ctx, userID, bundle, conn)
	if err != nil {
		return "", err
	}
	return refreshed.Access, nil
}

// NotionAnyAccessToken returns a valid access token from the most recently
// updated Notion connection. Used only for lazy tool discovery bootstrap.
func (s *Service) NotionAnyAccessToken(ctx context.Context) (string, error) {
	userID, bundle, conn, err := s.loadAnyNotionBundle(ctx)
	if err != nil {
		return "", err
	}
	refreshed, err := s.ensureFreshNotionBundle(ctx, userID, bundle, conn)
	if err != nil {
		return "", err
	}
	return refreshed.Access, nil
}

func (s *Service) loadNotionBundle(ctx context.Context, userID string) (notionTokenBundle, Connection, error) {
	conn, err := s.Store.Get(ctx, userID, ProviderNotion)
	if err != nil {
		return notionTokenBundle{}, Connection{}, err
	}
	raw, err := s.Store.RawToken(ctx, userID, ProviderNotion)
	if err != nil {
		return notionTokenBundle{}, Connection{}, err
	}
	bundle, err := parseNotionTokenBundle(raw)
	if err != nil {
		return notionTokenBundle{}, Connection{}, fmt.Errorf("parse notion token for %s: %w", userID, err)
	}
	return bundle, conn, nil
}

func (s *Service) loadAnyNotionBundle(ctx context.Context) (string, notionTokenBundle, Connection, error) {
	userID, raw, err := s.Store.AnyTokenUser(ctx, ProviderNotion)
	if err != nil {
		return "", notionTokenBundle{}, Connection{}, err
	}
	conn, err := s.Store.Get(ctx, userID, ProviderNotion)
	if err != nil {
		return "", notionTokenBundle{}, Connection{}, err
	}
	bundle, err := parseNotionTokenBundle(raw)
	if err != nil {
		return "", notionTokenBundle{}, Connection{}, fmt.Errorf("parse notion token for %s: %w", userID, err)
	}
	return userID, bundle, conn, nil
}

func (s *Service) ensureFreshNotionBundle(ctx context.Context, userID string, bundle notionTokenBundle, conn Connection) (notionTokenBundle, error) {
	now := time.Now().UTC()
	if !bundle.needsRefresh(now) {
		return bundle, nil
	}
	mu := s.notionRefreshMutex(userID)
	mu.Lock()
	defer mu.Unlock()

	if latest, latestConn, err := s.loadNotionBundle(ctx, userID); err == nil {
		bundle = latest
		conn = latestConn
		if !bundle.needsRefresh(time.Now().UTC()) {
			return bundle, nil
		}
	}

	if strings.TrimSpace(bundle.Refresh) == "" {
		return notionTokenBundle{}, s.Required(userID, ProviderNotion)
	}
	if !s.Config.NotionEnabled() {
		return notionTokenBundle{}, fmt.Errorf("notion oauth is not configured")
	}

	redirectURI := bundle.RedirectURI
	if redirectURI == "" {
		redirectURI = s.callbackURL(ProviderNotion)
	}
	clientID := bundle.ClientID
	if clientID == "" {
		var err error
		clientID, err = s.notion().ensureClient(ctx, redirectURI)
		if err != nil {
			return notionTokenBundle{}, err
		}
	}

	response, err := s.notion().refresh(ctx, bundle.Refresh, redirectURI, clientID)
	if err != nil {
		return notionTokenBundle{}, fmt.Errorf("refresh notion token: %w", err)
	}

	refresh := response.RefreshToken
	if refresh == "" {
		refresh = bundle.Refresh
	}
	updated := notionTokenBundle{
		Access:      response.AccessToken,
		Refresh:     refresh,
		ClientID:    clientID,
		RedirectURI: redirectURI,
		ExpiresAt:   notionExpiresAt(response.AccessToken, response.ExpiresIn),
	}
	stored, err := encodeNotionTokenBundle(updated)
	if err != nil {
		return notionTokenBundle{}, err
	}
	account := conn.Account
	if label := s.notion().accountLabel(response); label != "" {
		account = label
	}
	scopes := conn.Scopes
	if len(response.Scopes) > 0 {
		scopes = response.Scopes
	}
	if err := s.Store.UpsertToken(ctx, userID, ProviderNotion, stored, scopes, account); err != nil {
		return notionTokenBundle{}, err
	}
	return updated, nil
}

func (s *Service) maybeBackfillNotionAccount(ctx context.Context, userID string, bundle notionTokenBundle, conn Connection) {
	if strings.TrimSpace(conn.Account) != "" {
		return
	}
	label := accountFromJWT(bundle.Access)
	if label == "" {
		return
	}
	stored, err := encodeNotionTokenBundle(bundle)
	if err != nil {
		return
	}
	_ = s.Store.UpsertToken(ctx, userID, ProviderNotion, stored, conn.Scopes, label)
}

func (s *Service) notionRefreshMutex(userID string) *sync.Mutex {
	value, _ := s.mutableState().notionRefresh.LoadOrStore(userID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s *Service) storeNotionBundle(ctx context.Context, userID, clientID, redirectURI string, response notionTokenResponse, account string, scopes []string) error {
	bundle := notionTokenBundle{
		Access:      response.AccessToken,
		Refresh:     response.RefreshToken,
		ClientID:    clientID,
		RedirectURI: redirectURI,
		ExpiresAt:   notionExpiresAt(response.AccessToken, response.ExpiresIn),
	}
	stored, err := encodeNotionTokenBundle(bundle)
	if err != nil {
		return err
	}
	if label := s.notion().accountLabel(response); label != "" {
		account = label
	}
	return s.Store.UpsertToken(ctx, userID, ProviderNotion, stored, scopes, account)
}

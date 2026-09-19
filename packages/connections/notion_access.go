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
	return s.NotionAccessTokenInstance(ctx, userID, DefaultInstanceID)
}

func (s *Service) NotionAccessTokenInstance(ctx context.Context, userID, instanceID string) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", ErrNotConnected
	}
	bundle, conn, err := s.loadNotionBundleInstance(ctx, userID, instanceID)
	if err != nil {
		return "", err
	}
	s.maybeBackfillNotionAccount(ctx, userID, bundle, conn)
	refreshed, err := s.ensureFreshNotionBundleInstance(ctx, userID, bundle, conn)
	if err != nil {
		return "", err
	}
	return refreshed.Access, nil
}

// NotionMCPConnected reports whether the user has a Notion MCP OAuth token stored.
func (s *Service) NotionMCPConnected(ctx context.Context, userID string) bool {
	return s.NotionMCPConnectedInstance(ctx, userID, DefaultInstanceID)
}

func (s *Service) NotionMCPConnectedInstance(ctx context.Context, userID, instanceID string) bool {
	if s.Store == nil || strings.TrimSpace(userID) == "" {
		return false
	}
	raw, err := s.notionRawTokenInstance(ctx, userID, ProviderNotion, instanceID)
	if err != nil {
		return false
	}
	return notionMCPOAuthToken(raw)
}

func (s *Service) notionRawTokenInstance(ctx context.Context, userID, provider, instanceID string) (string, error) {
	if store, ok := instanceStore(s.Store); ok {
		return store.RawTokenInstance(ctx, userID, provider, instanceID)
	}
	if connectionInstanceID(instanceID) != DefaultInstanceID {
		return "", ErrNotConnected
	}
	return s.Store.RawToken(ctx, userID, provider)
}

func notionMCPOAuthToken(raw string) bool {
	bundle, err := parseNotionTokenBundle(raw)
	return err == nil && strings.TrimSpace(bundle.Refresh) != ""
}

func (s *Service) loadNotionBundle(ctx context.Context, userID string) (notionTokenBundle, Connection, error) {
	return s.loadNotionBundleInstance(ctx, userID, DefaultInstanceID)
}

func (s *Service) loadNotionBundleInstance(ctx context.Context, userID, instanceID string) (notionTokenBundle, Connection, error) {
	conn, err := s.GetConnection(ctx, userID, ProviderNotion, instanceID)
	if err != nil {
		return notionTokenBundle{}, Connection{}, err
	}
	raw, err := s.notionRawTokenInstance(ctx, userID, ProviderNotion, conn.InstanceID)
	if err != nil {
		return notionTokenBundle{}, Connection{}, err
	}
	bundle, err := parseNotionTokenBundle(raw)
	if err != nil {
		return notionTokenBundle{}, Connection{}, fmt.Errorf("parse notion token for %s: %w", userID, err)
	}
	return bundle, conn, nil
}

func (s *Service) ensureFreshNotionBundle(ctx context.Context, userID string, bundle notionTokenBundle, conn Connection) (notionTokenBundle, error) {
	return s.ensureFreshNotionBundleInstance(ctx, userID, bundle, conn)
}

func (s *Service) ensureFreshNotionBundleInstance(ctx context.Context, userID string, bundle notionTokenBundle, conn Connection) (notionTokenBundle, error) {
	now := time.Now().UTC()
	if !bundle.needsRefresh(now) {
		return bundle, nil
	}
	mu := s.notionRefreshMutex(userID + "\x00" + connectionInstanceID(conn.InstanceID))
	mu.Lock()
	defer mu.Unlock()

	if latest, latestConn, err := s.loadNotionBundleInstance(ctx, userID, conn.InstanceID); err == nil {
		bundle = latest
		conn = latestConn
		if !bundle.needsRefresh(time.Now().UTC()) {
			return bundle, nil
		}
	}

	if strings.TrimSpace(bundle.Refresh) == "" {
		return notionTokenBundle{}, s.RequiredInstance(userID, ProviderNotion, conn.InstanceID)
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
	if err := s.upsertToken(ctx, userID, ProviderNotion, conn.InstanceID, conn.Label, stored, scopes, account, conn.Metadata); err != nil {
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
	_ = s.upsertToken(ctx, userID, ProviderNotion, conn.InstanceID, conn.Label, stored, conn.Scopes, label, conn.Metadata)
}

func (s *Service) notionRefreshMutex(userID string) *sync.Mutex {
	value, _ := s.mutableState().notionRefresh.LoadOrStore(userID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s *Service) storeNotionBundle(ctx context.Context, userID, clientID, redirectURI string, response notionTokenResponse, account string, scopes []string) error {
	return s.storeNotionBundleInstance(ctx, userID, DefaultInstanceID, "", nil, clientID, redirectURI, response, account, scopes)
}

func (s *Service) storeNotionBundleInstance(ctx context.Context, userID, instanceID, label string, metadata map[string]string, clientID, redirectURI string, response notionTokenResponse, account string, scopes []string) error {
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
	return s.upsertToken(ctx, userID, ProviderNotion, instanceID, label, stored, scopes, account, metadata)
}

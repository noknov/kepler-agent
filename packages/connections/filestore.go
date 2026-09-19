package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type fileRecord struct {
	UserID     string            `json:"user_id"`
	Provider   string            `json:"provider"`
	InstanceID string            `json:"instance_id,omitempty"`
	Label      string            `json:"label,omitempty"`
	Status     Status            `json:"status"`
	Token      string            `json:"token_ciphertext"`
	Scopes     []string          `json:"scopes,omitempty"`
	Account    string            `json:"account,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type oauthStateRecord struct {
	State         string            `json:"state"`
	UserID        string            `json:"user_id"`
	Provider      string            `json:"provider"`
	InstanceID    string            `json:"instance_id,omitempty"`
	Label         string            `json:"label,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	ExpiresAt     time.Time         `json:"expires_at"`
	CodeVerifier  string            `json:"code_verifier,omitempty"`
	Origin        string            `json:"origin,omitempty"`
	ReturnContext string            `json:"return_context,omitempty"`
}

type FileStore struct {
	Path      string
	SecretKey string
	mu        sync.Mutex
}

func NewFileStore(path, secretKey string) (*FileStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return &FileStore{Path: path, SecretKey: secretKey}, nil
}

func (s *FileStore) load() (map[string]fileRecord, []oauthStateRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]fileRecord{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var payload struct {
		Connections []fileRecord       `json:"connections"`
		States      []oauthStateRecord `json:"oauth_states"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, nil, err
	}
	out := make(map[string]fileRecord, len(payload.Connections))
	for _, item := range payload.Connections {
		item.InstanceID = connectionInstanceID(item.InstanceID)
		out[fileConnectionKey(item.UserID, item.Provider, item.InstanceID)] = item
	}
	return out, payload.States, nil
}

func (s *FileStore) save(connections map[string]fileRecord, states []oauthStateRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]fileRecord, 0, len(connections))
	for _, item := range connections {
		items = append(items, item)
	}
	payload := struct {
		Connections []fileRecord       `json:"connections"`
		States      []oauthStateRecord `json:"oauth_states"`
	}{Connections: items, States: states}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, data, 0o600)
}

func (s *FileStore) Get(ctx context.Context, userID, provider string) (Connection, error) {
	return s.GetInstance(ctx, userID, provider, DefaultInstanceID)
}

func (s *FileStore) List(ctx context.Context, userID string) ([]Connection, error) {
	connections, _, err := s.load()
	if err != nil {
		return nil, err
	}
	var out []Connection
	for _, item := range connections {
		if item.UserID != userID {
			continue
		}
		out = append(out, fileConnection(item))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		left, right := strings.ToLower(InstanceDisplayName(out[i])), strings.ToLower(InstanceDisplayName(out[j]))
		if left != right {
			return left < right
		}
		return out[i].InstanceID < out[j].InstanceID
	})
	return out, nil
}

func (s *FileStore) UpsertToken(ctx context.Context, userID, provider, token string, scopes []string, account string) error {
	return s.UpsertTokenInstance(ctx, userID, provider, DefaultInstanceID, "", token, scopes, account, nil)
}

func (s *FileStore) GetInstance(ctx context.Context, userID, provider, instanceID string) (Connection, error) {
	instanceID = connectionInstanceID(instanceID)
	connections, _, err := s.load()
	if err != nil {
		return Connection{}, err
	}
	item, ok := connections[fileConnectionKey(userID, provider, instanceID)]
	if !ok {
		return Connection{}, ErrNotConnected
	}
	return fileConnection(item), nil
}

func (s *FileStore) ListInstances(ctx context.Context, userID string) ([]Connection, error) {
	return s.List(ctx, userID)
}

func (s *FileStore) UpsertTokenInstance(ctx context.Context, userID, provider, instanceID, label, token string, scopes []string, account string, metadata map[string]string) error {
	connections, states, err := s.load()
	if err != nil {
		return err
	}
	encrypted, err := encrypt(s.SecretKey, token)
	if err != nil {
		return err
	}
	instanceID = connectionInstanceID(instanceID)
	connections[fileConnectionKey(userID, provider, instanceID)] = fileRecord{
		UserID: userID, Provider: provider, InstanceID: instanceID, Label: label, Status: StatusConnected,
		Token: encrypted, Scopes: scopes, Account: account, Metadata: cloneMetadata(metadata), UpdatedAt: time.Now().UTC(),
	}
	return s.save(connections, states)
}

func (s *FileStore) Delete(ctx context.Context, userID, provider string) error {
	return s.DeleteInstance(ctx, userID, provider, DefaultInstanceID)
}

func (s *FileStore) DeleteInstance(ctx context.Context, userID, provider, instanceID string) error {
	instanceID = connectionInstanceID(instanceID)
	connections, states, err := s.load()
	if err != nil {
		return err
	}
	delete(connections, fileConnectionKey(userID, provider, instanceID))
	return s.save(connections, states)
}

func (s *FileStore) Token(ctx context.Context, userID, provider string) (string, error) {
	raw, err := s.RawToken(ctx, userID, provider)
	if err != nil {
		return "", err
	}
	return decodeStoredToken(raw), nil
}

func (s *FileStore) RawToken(ctx context.Context, userID, provider string) (string, error) {
	return s.RawTokenInstance(ctx, userID, provider, DefaultInstanceID)
}

func (s *FileStore) TokenInstance(ctx context.Context, userID, provider, instanceID string) (string, error) {
	raw, err := s.RawTokenInstance(ctx, userID, provider, instanceID)
	if err != nil {
		return "", err
	}
	return decodeStoredToken(raw), nil
}

func (s *FileStore) RawTokenInstance(ctx context.Context, userID, provider, instanceID string) (string, error) {
	instanceID = connectionInstanceID(instanceID)
	connections, _, err := s.load()
	if err != nil {
		return "", err
	}
	item, ok := connections[fileConnectionKey(userID, provider, instanceID)]
	if !ok || item.Status != StatusConnected {
		return "", ErrNotConnected
	}
	return decrypt(s.SecretKey, item.Token)
}

func (s *FileStore) AnyToken(ctx context.Context, provider string) (string, error) {
	_, raw, err := s.AnyTokenUser(ctx, provider)
	if err != nil {
		return "", err
	}
	if token := decodeStoredToken(raw); token != "" {
		return token, nil
	}
	return "", ErrNotConnected
}

func (s *FileStore) AnyTokenUser(ctx context.Context, provider string) (string, string, error) {
	connections, _, err := s.load()
	if err != nil {
		return "", "", err
	}
	var latest fileRecord
	var found bool
	for _, item := range connections {
		if item.Provider != provider || item.Status != StatusConnected {
			continue
		}
		if !found || item.UpdatedAt.After(latest.UpdatedAt) {
			latest = item
			found = true
		}
	}
	if !found {
		return "", "", ErrNotConnected
	}
	token, err := decrypt(s.SecretKey, latest.Token)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(token) == "" {
		return "", "", ErrNotConnected
	}
	return latest.UserID, token, nil
}

func (s *FileStore) CreateOAuthState(ctx context.Context, userID, provider, state string, expiresAt time.Time, meta OAuthStateMeta) error {
	connections, states, err := s.load()
	if err != nil {
		return err
	}
	states = append(states, oauthStateRecord{State: state, UserID: userID, Provider: provider, InstanceID: connectionInstanceID(meta.InstanceID), Label: meta.Label, Metadata: cloneMetadata(meta.Metadata), ExpiresAt: expiresAt, CodeVerifier: meta.CodeVerifier, Origin: meta.Origin, ReturnContext: meta.ReturnContext})
	return s.save(connections, states)
}

func (s *FileStore) PeekOAuthState(ctx context.Context, state string) (string, string, OAuthStateMeta, error) {
	_, states, err := s.load()
	if err != nil {
		return "", "", OAuthStateMeta{}, err
	}
	now := time.Now().UTC()
	for _, item := range states {
		if item.State == state && item.ExpiresAt.After(now) {
			return item.UserID, item.Provider, OAuthStateMeta{InstanceID: connectionInstanceID(item.InstanceID), Label: item.Label, Metadata: cloneMetadata(item.Metadata), CodeVerifier: item.CodeVerifier, Origin: item.Origin, ReturnContext: item.ReturnContext}, nil
		}
	}
	return "", "", OAuthStateMeta{}, fmt.Errorf("oauth state is invalid or expired")
}

func (s *FileStore) ConsumeOAuthState(ctx context.Context, state string) (string, string, OAuthStateMeta, error) {
	connections, states, err := s.load()
	if err != nil {
		return "", "", OAuthStateMeta{}, err
	}
	now := time.Now().UTC()
	var userID, provider string
	var meta OAuthStateMeta
	remaining := states[:0]
	for _, item := range states {
		if item.State == state {
			if item.ExpiresAt.After(now) {
				userID, provider = item.UserID, item.Provider
				meta = OAuthStateMeta{InstanceID: connectionInstanceID(item.InstanceID), Label: item.Label, Metadata: cloneMetadata(item.Metadata), CodeVerifier: item.CodeVerifier, Origin: item.Origin, ReturnContext: item.ReturnContext}
				continue
			}
			return "", "", OAuthStateMeta{}, fmt.Errorf("oauth state is invalid or expired")
		}
		remaining = append(remaining, item)
	}
	if userID == "" {
		return "", "", OAuthStateMeta{}, fmt.Errorf("oauth state is invalid or expired")
	}
	if err := s.save(connections, remaining); err != nil {
		return "", "", OAuthStateMeta{}, err
	}
	return userID, provider, meta, nil
}

func fileConnectionKey(userID, provider, instanceID string) string {
	return userID + "\x00" + provider + "\x00" + connectionInstanceID(instanceID)
}

func fileConnection(item fileRecord) Connection {
	return Connection{UserID: item.UserID, Provider: item.Provider, InstanceID: connectionInstanceID(item.InstanceID), Label: item.Label, Status: item.Status, Scopes: append([]string(nil), item.Scopes...), Account: item.Account, Metadata: cloneMetadata(item.Metadata), UpdatedAt: item.UpdatedAt}
}

func cloneMetadata(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string]string, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

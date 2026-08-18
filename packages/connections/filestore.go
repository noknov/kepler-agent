package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type fileRecord struct {
	UserID    string    `json:"user_id"`
	Provider  string    `json:"provider"`
	Status    Status    `json:"status"`
	Token     string    `json:"token_ciphertext"`
	Scopes    []string  `json:"scopes,omitempty"`
	Account   string    `json:"account,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type oauthStateRecord struct {
	State        string    `json:"state"`
	UserID       string    `json:"user_id"`
	Provider     string    `json:"provider"`
	ExpiresAt    time.Time `json:"expires_at"`
	CodeVerifier string    `json:"code_verifier,omitempty"`
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
		out[item.UserID+"\x00"+item.Provider] = item
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
	connections, _, err := s.load()
	if err != nil {
		return Connection{}, err
	}
	item, ok := connections[userID+"\x00"+provider]
	if !ok {
		return Connection{}, ErrNotConnected
	}
	return Connection{UserID: item.UserID, Provider: item.Provider, Status: item.Status, Scopes: item.Scopes, Account: item.Account, UpdatedAt: item.UpdatedAt}, nil
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
		out = append(out, Connection{UserID: item.UserID, Provider: item.Provider, Status: item.Status, Scopes: item.Scopes, Account: item.Account, UpdatedAt: item.UpdatedAt})
	}
	return out, nil
}

func (s *FileStore) UpsertToken(ctx context.Context, userID, provider, token string, scopes []string, account string) error {
	connections, states, err := s.load()
	if err != nil {
		return err
	}
	encrypted, err := encrypt(s.SecretKey, token)
	if err != nil {
		return err
	}
	connections[userID+"\x00"+provider] = fileRecord{
		UserID: userID, Provider: provider, Status: StatusConnected,
		Token: encrypted, Scopes: scopes, Account: account, UpdatedAt: time.Now().UTC(),
	}
	return s.save(connections, states)
}

func (s *FileStore) Delete(ctx context.Context, userID, provider string) error {
	connections, states, err := s.load()
	if err != nil {
		return err
	}
	delete(connections, userID+"\x00"+provider)
	return s.save(connections, states)
}

func (s *FileStore) Token(ctx context.Context, userID, provider string) (string, error) {
	connections, _, err := s.load()
	if err != nil {
		return "", err
	}
	item, ok := connections[userID+"\x00"+provider]
	if !ok || item.Status != StatusConnected {
		return "", ErrNotConnected
	}
	token, err := decrypt(s.SecretKey, item.Token)
	if err != nil {
		return "", err
	}
	return decodeStoredToken(token), nil
}

func (s *FileStore) AnyToken(ctx context.Context, provider string) (string, error) {
	connections, _, err := s.load()
	if err != nil {
		return "", err
	}
	for _, item := range connections {
		if item.Provider != provider || item.Status != StatusConnected {
			continue
		}
		token, err := decrypt(s.SecretKey, item.Token)
		if err != nil {
			continue
		}
		if token = decodeStoredToken(token); token != "" {
			return token, nil
		}
	}
	return "", ErrNotConnected
}

func (s *FileStore) CreateOAuthState(ctx context.Context, userID, provider, state string, expiresAt time.Time, meta OAuthStateMeta) error {
	connections, states, err := s.load()
	if err != nil {
		return err
	}
	states = append(states, oauthStateRecord{State: state, UserID: userID, Provider: provider, ExpiresAt: expiresAt, CodeVerifier: meta.CodeVerifier})
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
			return item.UserID, item.Provider, OAuthStateMeta{CodeVerifier: item.CodeVerifier}, nil
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
				meta = OAuthStateMeta{CodeVerifier: item.CodeVerifier}
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

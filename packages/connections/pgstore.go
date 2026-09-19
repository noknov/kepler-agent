package connections

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PGStore struct {
	Pool      *pgxpool.Pool
	SecretKey string
}

func (s PGStore) Get(ctx context.Context, userID, provider string) (Connection, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT user_id, provider, instance_id, label, status, scopes, account, metadata, updated_at
		FROM user_connections
		WHERE user_id = $1 AND provider = $2 AND instance_id = $3`, userID, provider, DefaultInstanceID)
	var conn Connection
	var scopes []string
	if err := row.Scan(&conn.UserID, &conn.Provider, &conn.InstanceID, &conn.Label, &conn.Status, &scopes, &conn.Account, &conn.Metadata, &conn.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Connection{}, ErrNotConnected
		}
		return Connection{}, err
	}
	conn.Scopes = scopes
	return conn, nil
}

func (s PGStore) List(ctx context.Context, userID string) ([]Connection, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT user_id, provider, instance_id, label, status, scopes, account, metadata, updated_at
		FROM user_connections
		WHERE user_id = $1
		ORDER BY provider`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		var conn Connection
		var scopes []string
		if err := rows.Scan(&conn.UserID, &conn.Provider, &conn.InstanceID, &conn.Label, &conn.Status, &scopes, &conn.Account, &conn.Metadata, &conn.UpdatedAt); err != nil {
			return nil, err
		}
		conn.Scopes = scopes
		out = append(out, conn)
	}
	return out, rows.Err()
}

func (s PGStore) UpsertToken(ctx context.Context, userID, provider, token string, scopes []string, account string) error {
	return s.UpsertTokenInstance(ctx, userID, provider, DefaultInstanceID, "", token, scopes, account, nil)
}

func (s PGStore) GetInstance(ctx context.Context, userID, provider, instanceID string) (Connection, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT user_id, provider, instance_id, label, status, scopes, account, metadata, updated_at
		FROM user_connections
		WHERE user_id = $1 AND provider = $2 AND instance_id = $3`, userID, provider, connectionInstanceID(instanceID))
	var conn Connection
	var scopes []string
	if err := row.Scan(&conn.UserID, &conn.Provider, &conn.InstanceID, &conn.Label, &conn.Status, &scopes, &conn.Account, &conn.Metadata, &conn.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Connection{}, ErrNotConnected
		}
		return Connection{}, err
	}
	conn.Scopes = scopes
	return conn, nil
}

func (s PGStore) ListInstances(ctx context.Context, userID string) ([]Connection, error) {
	return s.List(ctx, userID)
}

func (s PGStore) UpsertTokenInstance(ctx context.Context, userID, provider, instanceID, label, token string, scopes []string, account string, metadata map[string]string) error {
	encrypted, err := encrypt(s.SecretKey, token)
	if err != nil {
		return err
	}
	scopes = scopesForStorage(scopes)
	instanceID = connectionInstanceID(instanceID)
	if metadata == nil {
		metadata = map[string]string{}
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO user_connections (user_id, provider, instance_id, label, status, token_ciphertext, scopes, account, metadata, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		ON CONFLICT (user_id, provider, instance_id) DO UPDATE SET
			label = EXCLUDED.label,
			status = EXCLUDED.status,
			token_ciphertext = EXCLUDED.token_ciphertext,
			scopes = EXCLUDED.scopes,
			account = EXCLUDED.account,
			metadata = EXCLUDED.metadata,
			updated_at = NOW()`,
		userID, provider, instanceID, label, StatusConnected, encrypted, scopes, account, metadata)
	return err
}

// scopesForStorage keeps the PostgreSQL user_connections.scopes invariant:
// pgx encodes a nil []string as SQL NULL, while an OAuth provider without
// scopes must be stored as the empty SQL array ('{}').
func scopesForStorage(scopes []string) []string {
	if scopes == nil {
		return []string{}
	}
	return scopes
}

func (s PGStore) Delete(ctx context.Context, userID, provider string) error {
	return s.DeleteInstance(ctx, userID, provider, DefaultInstanceID)
}

func (s PGStore) DeleteInstance(ctx context.Context, userID, provider, instanceID string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM user_connections WHERE user_id = $1 AND provider = $2 AND instance_id = $3`, userID, provider, connectionInstanceID(instanceID))
	return err
}

func (s PGStore) Token(ctx context.Context, userID, provider string) (string, error) {
	raw, err := s.RawToken(ctx, userID, provider)
	if err != nil {
		return "", err
	}
	return decodeStoredToken(raw), nil
}

func (s PGStore) RawToken(ctx context.Context, userID, provider string) (string, error) {
	return s.RawTokenInstance(ctx, userID, provider, DefaultInstanceID)
}

func (s PGStore) TokenInstance(ctx context.Context, userID, provider, instanceID string) (string, error) {
	raw, err := s.RawTokenInstance(ctx, userID, provider, instanceID)
	if err != nil {
		return "", err
	}
	return decodeStoredToken(raw), nil
}

func (s PGStore) RawTokenInstance(ctx context.Context, userID, provider, instanceID string) (string, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT token_ciphertext, status
		FROM user_connections
		WHERE user_id = $1 AND provider = $2 AND instance_id = $3`, userID, provider, connectionInstanceID(instanceID))
	var ciphertext string
	var status Status
	if err := row.Scan(&ciphertext, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotConnected
		}
		return "", err
	}
	if status != StatusConnected {
		return "", ErrNotConnected
	}
	token, err := decrypt(s.SecretKey, ciphertext)
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s PGStore) AnyToken(ctx context.Context, provider string) (string, error) {
	_, raw, err := s.AnyTokenUser(ctx, provider)
	if err != nil {
		return "", err
	}
	if token := decodeStoredToken(raw); token != "" {
		return token, nil
	}
	return "", ErrNotConnected
}

func (s PGStore) AnyTokenUser(ctx context.Context, provider string) (string, string, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT user_id, token_ciphertext
		FROM user_connections
		WHERE provider = $1 AND status = $2
		ORDER BY updated_at DESC
		LIMIT 1`, provider, StatusConnected)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", "", ErrNotConnected
	}
	var userID, ciphertext string
	if err := rows.Scan(&userID, &ciphertext); err != nil {
		return "", "", err
	}
	token, err := decrypt(s.SecretKey, ciphertext)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(token) == "" {
		return "", "", ErrNotConnected
	}
	return userID, token, rows.Err()
}

func (s PGStore) CreateOAuthState(ctx context.Context, userID, provider, state string, expiresAt time.Time, meta OAuthStateMeta) error {
	if meta.Metadata == nil {
		meta.Metadata = map[string]string{}
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO oauth_states (state, user_id, provider, instance_id, label, metadata, origin, return_context, expires_at, code_verifier)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, state, userID, provider, connectionInstanceID(meta.InstanceID), meta.Label, meta.Metadata, meta.Origin, meta.ReturnContext, expiresAt, meta.CodeVerifier)
	return err
}

func (s PGStore) PeekOAuthState(ctx context.Context, state string) (string, string, OAuthStateMeta, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT user_id, provider, instance_id, label, metadata, origin, return_context, code_verifier
		FROM oauth_states
		WHERE state = $1 AND expires_at > NOW()`, state)
	var userID, provider, instanceID, label, origin, returnContext, codeVerifier string
	var metadata map[string]string
	if err := row.Scan(&userID, &provider, &instanceID, &label, &metadata, &origin, &returnContext, &codeVerifier); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", OAuthStateMeta{}, fmt.Errorf("oauth state is invalid or expired")
		}
		return "", "", OAuthStateMeta{}, err
	}
	return userID, provider, OAuthStateMeta{InstanceID: connectionInstanceID(instanceID), Label: label, Metadata: metadata, CodeVerifier: codeVerifier, Origin: origin, ReturnContext: returnContext}, nil
}

func (s PGStore) ConsumeOAuthState(ctx context.Context, state string) (string, string, OAuthStateMeta, error) {
	row := s.Pool.QueryRow(ctx, `
		DELETE FROM oauth_states
		WHERE state = $1 AND expires_at > NOW()
		RETURNING user_id, provider, instance_id, label, metadata, origin, return_context, code_verifier`, state)
	var userID, provider, instanceID, label, origin, returnContext, codeVerifier string
	var metadata map[string]string
	if err := row.Scan(&userID, &provider, &instanceID, &label, &metadata, &origin, &returnContext, &codeVerifier); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", OAuthStateMeta{}, fmt.Errorf("oauth state is invalid or expired")
		}
		return "", "", OAuthStateMeta{}, err
	}
	return userID, provider, OAuthStateMeta{InstanceID: connectionInstanceID(instanceID), Label: label, Metadata: metadata, CodeVerifier: codeVerifier, Origin: origin, ReturnContext: returnContext}, nil
}

func StatusMap(connections []Connection) map[string]Connection {
	out := make(map[string]Connection, len(connections))
	for _, item := range connections {
		out[item.Provider] = item
	}
	return out
}

func IsConnected(status map[string]Connection, provider string) bool {
	item, ok := status[provider]
	return ok && item.Status == StatusConnected && strings.TrimSpace(item.UserID) != ""
}

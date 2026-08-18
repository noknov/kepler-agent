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
		SELECT user_id, provider, status, scopes, account, updated_at
		FROM user_connections
		WHERE user_id = $1 AND provider = $2`, userID, provider)
	var conn Connection
	var scopes []string
	if err := row.Scan(&conn.UserID, &conn.Provider, &conn.Status, &scopes, &conn.Account, &conn.UpdatedAt); err != nil {
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
		SELECT user_id, provider, status, scopes, account, updated_at
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
		if err := rows.Scan(&conn.UserID, &conn.Provider, &conn.Status, &scopes, &conn.Account, &conn.UpdatedAt); err != nil {
			return nil, err
		}
		conn.Scopes = scopes
		out = append(out, conn)
	}
	return out, rows.Err()
}

func (s PGStore) UpsertToken(ctx context.Context, userID, provider, token string, scopes []string, account string) error {
	encrypted, err := encrypt(s.SecretKey, token)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO user_connections (user_id, provider, status, token_ciphertext, scopes, account, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (user_id, provider) DO UPDATE SET
			status = EXCLUDED.status,
			token_ciphertext = EXCLUDED.token_ciphertext,
			scopes = EXCLUDED.scopes,
			account = EXCLUDED.account,
			updated_at = NOW()`,
		userID, provider, StatusConnected, encrypted, scopes, account)
	return err
}

func (s PGStore) Delete(ctx context.Context, userID, provider string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM user_connections WHERE user_id = $1 AND provider = $2`, userID, provider)
	return err
}

func (s PGStore) Token(ctx context.Context, userID, provider string) (string, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT token_ciphertext, status
		FROM user_connections
		WHERE user_id = $1 AND provider = $2`, userID, provider)
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
	return decrypt(s.SecretKey, ciphertext)
}

func (s PGStore) CreateOAuthState(ctx context.Context, userID, provider, state string, expiresAt time.Time) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO oauth_states (state, user_id, provider, expires_at)
		VALUES ($1, $2, $3, $4)`, state, userID, provider, expiresAt)
	return err
}

func (s PGStore) PeekOAuthState(ctx context.Context, state string) (string, string, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT user_id, provider
		FROM oauth_states
		WHERE state = $1 AND expires_at > NOW()`, state)
	var userID, provider string
	if err := row.Scan(&userID, &provider); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", fmt.Errorf("oauth state is invalid or expired")
		}
		return "", "", err
	}
	return userID, provider, nil
}

func (s PGStore) ConsumeOAuthState(ctx context.Context, state string) (string, string, error) {
	row := s.Pool.QueryRow(ctx, `
		DELETE FROM oauth_states
		WHERE state = $1 AND expires_at > NOW()
		RETURNING user_id, provider`, state)
	var userID, provider string
	if err := row.Scan(&userID, &provider); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", fmt.Errorf("oauth state is invalid or expired")
		}
		return "", "", err
	}
	return userID, provider, nil
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

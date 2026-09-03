// Package web implements the authenticated hosted browser surface.
package web

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Identity struct {
	Provider    string `json:"provider"`
	TenantID    string `json:"tenantId"`
	SubjectID   string `json:"subjectId"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
}

func (i Identity) Key() string {
	return i.Provider + ":" + i.TenantID + ":" + i.SubjectID
}

type AuthState struct {
	Provider     string
	Nonce        string
	CodeVerifier string
	ReturnTo     string
	ExpiresAt    time.Time
}

type BrowserSession struct {
	Identity  Identity
	CreatedAt time.Time
	ExpiresAt time.Time
}

type Conversation struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	HasMessages bool       `json:"hasMessages,omitempty"`
	ArchivedAt  *time.Time `json:"archivedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type Store interface {
	CreateAuthState(context.Context, []byte, AuthState) error
	ConsumeAuthState(context.Context, []byte, time.Time) (AuthState, error)
	CreateSession(context.Context, []byte, BrowserSession) error
	GetSession(context.Context, []byte, time.Time) (BrowserSession, error)
	DeleteSession(context.Context, []byte) error
	CreateConversation(context.Context, Identity, Conversation) error
	GetConversation(context.Context, Identity, string) (Conversation, error)
	ListConversations(context.Context, Identity, bool, int, int) ([]Conversation, error)
	RenameConversation(context.Context, Identity, string, string) error
	ArchiveConversation(context.Context, Identity, string, bool) error
	TouchConversation(context.Context, Identity, string, string) error
}

var ErrNotFound = errors.New("not found")

type PGStore struct{ Pool *pgxpool.Pool }

func HashOpaque(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func (s PGStore) CreateAuthState(ctx context.Context, stateHash []byte, state AuthState) error {
	if s.Pool == nil {
		return fmt.Errorf("web store unavailable")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO web_auth_states(state_hash,provider,nonce,code_verifier,return_to,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, stateHash, state.Provider, state.Nonce, state.CodeVerifier, state.ReturnTo, state.ExpiresAt)
	return err
}

func (s PGStore) ConsumeAuthState(ctx context.Context, stateHash []byte, now time.Time) (AuthState, error) {
	if s.Pool == nil {
		return AuthState{}, fmt.Errorf("web store unavailable")
	}
	var state AuthState
	err := s.Pool.QueryRow(ctx, `UPDATE web_auth_states SET consumed_at=$2 WHERE state_hash=$1 AND consumed_at IS NULL AND expires_at>$2 RETURNING provider,nonce,code_verifier,return_to,expires_at`, stateHash, now).Scan(&state.Provider, &state.Nonce, &state.CodeVerifier, &state.ReturnTo, &state.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthState{}, ErrNotFound
	}
	return state, err
}

func (s PGStore) CreateSession(ctx context.Context, tokenHash []byte, session BrowserSession) error {
	if s.Pool == nil {
		return fmt.Errorf("web store unavailable")
	}
	i := session.Identity
	_, err := s.Pool.Exec(ctx, `INSERT INTO web_auth_sessions(token_hash,provider,tenant_id,subject_id,email,display_name,avatar_url,created_at,expires_at,last_seen_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$8)`, tokenHash, i.Provider, i.TenantID, i.SubjectID, i.Email, i.DisplayName, i.AvatarURL, session.CreatedAt, session.ExpiresAt)
	return err
}

func (s PGStore) GetSession(ctx context.Context, tokenHash []byte, now time.Time) (BrowserSession, error) {
	if s.Pool == nil {
		return BrowserSession{}, fmt.Errorf("web store unavailable")
	}
	var session BrowserSession
	err := s.Pool.QueryRow(ctx, `UPDATE web_auth_sessions SET last_seen_at=$2 WHERE token_hash=$1 AND expires_at>$2 RETURNING provider,tenant_id,subject_id,email,display_name,avatar_url,created_at,expires_at`, tokenHash, now).Scan(&session.Identity.Provider, &session.Identity.TenantID, &session.Identity.SubjectID, &session.Identity.Email, &session.Identity.DisplayName, &session.Identity.AvatarURL, &session.CreatedAt, &session.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return BrowserSession{}, ErrNotFound
	}
	return session, err
}

func (s PGStore) DeleteSession(ctx context.Context, tokenHash []byte) error {
	if s.Pool == nil {
		return fmt.Errorf("web store unavailable")
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM web_auth_sessions WHERE token_hash=$1`, tokenHash)
	return err
}

func (s PGStore) CreateConversation(ctx context.Context, owner Identity, conversation Conversation) error {
	if s.Pool == nil {
		return fmt.Errorf("web store unavailable")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO web_conversations(id,owner_provider,owner_tenant_id,owner_subject_id,title,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, conversation.ID, owner.Provider, owner.TenantID, owner.SubjectID, conversation.Title, conversation.CreatedAt, conversation.UpdatedAt)
	return err
}

func (s PGStore) GetConversation(ctx context.Context, owner Identity, id string) (Conversation, error) {
	if s.Pool == nil {
		return Conversation{}, fmt.Errorf("web store unavailable")
	}
	var conversation Conversation
	err := s.Pool.QueryRow(ctx, `SELECT id,title,archived_at,created_at,updated_at FROM web_conversations WHERE id=$1 AND owner_provider=$2 AND owner_tenant_id=$3 AND owner_subject_id=$4`, id, owner.Provider, owner.TenantID, owner.SubjectID).Scan(&conversation.ID, &conversation.Title, &conversation.ArchivedAt, &conversation.CreatedAt, &conversation.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	return conversation, err
}

func (s PGStore) ListConversations(ctx context.Context, owner Identity, archived bool, limit, offset int) ([]Conversation, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("web store unavailable")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,title,archived_at,created_at,updated_at FROM web_conversations WHERE owner_provider=$1 AND owner_tenant_id=$2 AND owner_subject_id=$3 AND (($4 AND archived_at IS NOT NULL) OR (NOT $4 AND archived_at IS NULL)) ORDER BY updated_at DESC LIMIT $5 OFFSET $6`, owner.Provider, owner.TenantID, owner.SubjectID, archived, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var conversations []Conversation
	for rows.Next() {
		var conversation Conversation
		if err := rows.Scan(&conversation.ID, &conversation.Title, &conversation.ArchivedAt, &conversation.CreatedAt, &conversation.UpdatedAt); err != nil {
			return nil, err
		}
		conversations = append(conversations, conversation)
	}
	return conversations, rows.Err()
}

func (s PGStore) RenameConversation(ctx context.Context, owner Identity, id, title string) error {
	return s.updateOwned(ctx, owner, id, `title=$5,updated_at=NOW()`, title)
}

func (s PGStore) ArchiveConversation(ctx context.Context, owner Identity, id string, archived bool) error {
	value := any(nil)
	if archived {
		value = time.Now().UTC()
	}
	return s.updateOwned(ctx, owner, id, `archived_at=$5,updated_at=NOW()`, value)
}

func (s PGStore) TouchConversation(ctx context.Context, owner Identity, id, title string) error {
	if s.Pool == nil {
		return fmt.Errorf("web store unavailable")
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE web_conversations SET title=CASE WHEN title='New conversation' AND $5<>'' THEN $5 ELSE title END,updated_at=NOW() WHERE id=$1 AND owner_provider=$2 AND owner_tenant_id=$3 AND owner_subject_id=$4 AND archived_at IS NULL`, id, owner.Provider, owner.TenantID, owner.SubjectID, title)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s PGStore) updateOwned(ctx context.Context, owner Identity, id, set string, value any) error {
	if s.Pool == nil {
		return fmt.Errorf("web store unavailable")
	}
	query := `UPDATE web_conversations SET ` + set + ` WHERE id=$1 AND owner_provider=$2 AND owner_tenant_id=$3 AND owner_subject_id=$4`
	tag, err := s.Pool.Exec(ctx, query, id, owner.Provider, owner.TenantID, owner.SubjectID, value)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

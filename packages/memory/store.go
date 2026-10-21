// Package memory stores explicit user memories separately from rules, skills,
// and conversation history. A generation fences writes after a user reset.
package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Identity struct{ TeamID, UserID string }

func (i Identity) Validate() error {
	if strings.TrimSpace(i.TeamID) == "" || strings.TrimSpace(i.UserID) == "" {
		return fmt.Errorf("memory requires authenticated workspace and user identity")
	}
	return nil
}

type Record struct {
	Key       string    `json:"semantic_key"`
	Kind      string    `json:"kind"`
	Content   string    `json:"content"`
	SessionID string    `json:"source_session_id"`
	TurnID    string    `json:"source_turn_id"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store interface {
	Query(context.Context, Identity, Query) (Page, error)
	Put(context.Context, Identity, int64, Record) error
	Forget(context.Context, Identity, int64, string) error
	Reset(context.Context, Identity) error
}

type PGStore struct{ Pool *pgxpool.Pool }

func (s PGStore) Put(ctx context.Context, identity Identity, generation int64, record Record) error {
	if record.Version < 0 {
		return fmt.Errorf("memory version cannot be negative")
	}
	if strings.TrimSpace(record.Key) == "" || strings.TrimSpace(record.Content) == "" || len(record.Key) > 200 || len(record.Content) > 8000 {
		return fmt.Errorf("memory key/content is missing or exceeds its limit")
	}
	if record.Kind != "preference" && record.Kind != "project" && record.Kind != "procedure" && record.Kind != "fact" {
		return fmt.Errorf("unsupported memory kind")
	}
	return s.mutate(ctx, identity, generation, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO user_memories(team_id,user_id,generation,semantic_key,kind,content,source_session_id,source_turn_id)
SELECT $1,$2,$3,$4,$5,$6,$7,$8
WHERE $9::bigint=0 OR EXISTS(SELECT 1 FROM user_memories WHERE team_id=$1 AND user_id=$2 AND semantic_key=$4 AND version=$9)
ON CONFLICT(team_id,user_id,semantic_key) DO UPDATE SET generation=EXCLUDED.generation,kind=EXCLUDED.kind,content=EXCLUDED.content,source_session_id=EXCLUDED.source_session_id,source_turn_id=EXCLUDED.source_turn_id,version=user_memories.version+1,updated_at=NOW()
WHERE $9::bigint=0 OR user_memories.version=$9`, identity.TeamID, identity.UserID, generation, record.Key, record.Kind, record.Content, record.SessionID, record.TurnID, record.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("memory version conflict; read the current memory before updating")
		}
		return nil
	})
}

func (s PGStore) Forget(ctx context.Context, identity Identity, generation int64, key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("memory key is required")
	}
	return s.mutate(ctx, identity, generation, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM user_memories WHERE team_id=$1 AND user_id=$2 AND semantic_key=$3`, identity.TeamID, identity.UserID, key)
		return err
	})
}

func (s PGStore) mutate(ctx context.Context, identity Identity, generation int64, mutate func(pgx.Tx) error) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if s.Pool == nil {
		return fmt.Errorf("memory store unavailable")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `INSERT INTO user_memory_generations(team_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, identity.TeamID, identity.UserID); err != nil {
		return err
	}
	var current int64
	if err = tx.QueryRow(ctx, `SELECT generation FROM user_memory_generations WHERE team_id=$1 AND user_id=$2 FOR UPDATE`, identity.TeamID, identity.UserID).Scan(&current); err != nil {
		return err
	}
	if current != generation {
		return fmt.Errorf("memory was reset during this turn; old writes are rejected")
	}
	err = mutate(tx)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s PGStore) Reset(ctx context.Context, identity Identity) error {
	return s.ResetOnce(ctx, identity, "")
}

// ResetOnce makes retries harmless even if new memories were saved afterward.
func (s PGStore) ResetOnce(ctx context.Context, identity Identity, requestID string) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if s.Pool == nil {
		return fmt.Errorf("memory store unavailable")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if requestID != "" {
		tag, err := tx.Exec(ctx, `INSERT INTO user_memory_reset_requests(team_id,user_id,request_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, identity.TeamID, identity.UserID, requestID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO user_memory_generations(team_id,user_id,generation) VALUES($1,$2,1)
ON CONFLICT(team_id,user_id) DO UPDATE SET generation=user_memory_generations.generation+1`, identity.TeamID, identity.UserID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_memories WHERE team_id=$1 AND user_id=$2`, identity.TeamID, identity.UserID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Bound limits prompt injection by bytes while preserving the database ranking.
// Search and pagination are separate from the prompt's disclosure budget.
func Bound(records []Record, maxBytes int) []Record {
	var selected []Record
	size := 0
	for _, record := range records {
		cost := len(record.Key) + len(record.Content)
		if size+cost > maxBytes {
			continue
		}
		selected = append(selected, record)
		size += cost
	}
	return selected
}

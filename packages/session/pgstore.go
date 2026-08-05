package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the production session store. Session state is a single JSONB
// document so schema evolution remains compatible with old conversations.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore uses a shared pool. Database schema is an external runtime
// contract; stores never execute DDL.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func (s *PGStore) Get(ctx context.Context, id string) (Session, bool, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT payload FROM agent_sessions WHERE id=$1`, id).Scan(&data)
	if err == pgx.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	var out Session
	if err := json.Unmarshal(data, &out); err != nil {
		return Session{}, false, fmt.Errorf("decode session %s: %w", id, err)
	}
	return out, true, nil
}

func (s *PGStore) Save(ctx context.Context, sess Session) error {
	now := time.Now().UTC()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	sess.UpdatedAt = now
	data, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	// PostgreSQL JSONB rejects the NUL Unicode code point. Historical tool and
	// terminal output can contain it, so normalize at the persistence boundary.
	data = bytes.ReplaceAll(data, []byte(`\u0000`), nil)
	_, err = s.pool.Exec(ctx, `INSERT INTO agent_sessions (id,payload,created_at,updated_at) VALUES ($1,$2,$3,$4)
ON CONFLICT (id) DO UPDATE SET payload=EXCLUDED.payload, updated_at=EXCLUDED.updated_at`, sess.ID, data, sess.CreatedAt, sess.UpdatedAt)
	return err
}

// Lock acquires a PostgreSQL advisory lock on a dedicated connection. It
// serializes a complete agent turn across replicas without holding a pool
// connection while waiting for another worker to release the same session.
func (s *PGStore) Lock(ctx context.Context, id string) (func(), error) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	key := int64(h.Sum64())
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		var locked bool
		err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked)
		if err != nil {
			conn.Release()
			return nil, err
		}
		if locked {
			return func() {
				unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, key)
				conn.Release()
			}, nil
		}
		conn.Release()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

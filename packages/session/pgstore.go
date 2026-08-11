package session

import (
	"context"
	"hash/fnv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore owns the cross-process turn lock. Conversation state lives in the
// append-only agent transcript rather than a mutable session document.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore uses a shared pool. Database schema is an external runtime
// contract; stores never execute DDL.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

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

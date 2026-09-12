package session

import (
	"context"
	"hash/fnv"
	"sync"
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
	conn, key, err := s.acquire(ctx, id)
	if err != nil {
		return nil, err
	}
	return func() { release(conn, key) }, nil
}

// LockContext monitors the dedicated advisory-lock connection and cancels the
// execution context when PostgreSQL can no longer confirm that connection.
func (s *PGStore) LockContext(ctx context.Context, id string) (context.Context, func(), error) {
	conn, key, err := s.acquire(ctx, id)
	if err != nil {
		return ctx, nil, err
	}
	guarded, cancel := context.WithCancel(ctx)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-guarded.Done():
				return
			case <-ticker.C:
				var one int
				if err := conn.QueryRow(guarded, `SELECT 1`).Scan(&one); err != nil || one != 1 {
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	return guarded, func() {
		once.Do(func() {
			close(stop)
			<-done
			cancel()
			release(conn, key)
		})
	}, nil
}

func (s *PGStore) acquire(ctx context.Context, id string) (*pgxpool.Conn, int64, error) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	key := int64(h.Sum64())
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			return nil, 0, err
		}
		var locked bool
		err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked)
		if err != nil {
			conn.Release()
			return nil, 0, err
		}
		if locked {
			return conn, key, nil
		}
		conn.Release()
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-ticker.C:
		}
	}
}

func release(conn *pgxpool.Conn, key int64) {
	unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, key)
	conn.Release()
}

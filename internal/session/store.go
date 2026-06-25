package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wati/oncall-agent/internal/memory"
)

type Session struct {
	ID               string        `json:"id"`
	Channel          string        `json:"channel"`
	ThreadTS         string        `json:"thread_ts"`
	UserID           string        `json:"user_id"`
	Locale           string        `json:"locale,omitempty"`
	Summary          string        `json:"summary,omitempty"`
	Turns            []memory.Turn `json:"turns,omitempty"`
	PendingUserInput bool          `json:"pending_user_input,omitempty"`
	PendingUserID    string        `json:"pending_user_id,omitempty"`
	PendingQuestion  string        `json:"pending_question,omitempty"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

type Store interface {
	Get(ctx context.Context, id string) (Session, bool, error)
	Save(ctx context.Context, s Session) error
}

// FileStore persists sessions as JSON files with per-session locking.
// Unlike a global mutex, concurrent sessions on different threads never
// contend with each other — matching Claude Code's session isolation.
type FileStore struct {
	dir string

	mu    sync.Mutex
	locks map[string]*sessionLock
}

type sessionLock struct {
	sync.Mutex
	lastUsed time.Time
}

const lockEvictAge = 30 * time.Minute

func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &FileStore{dir: dir, locks: make(map[string]*sessionLock)}, nil
}

func ID(channel, threadTS string) string {
	raw := channel + ":" + threadTS
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func (s *FileStore) lockFor(id string) *sessionLock {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Evict stale locks periodically (amortized O(1) — only when map is large)
	if len(s.locks) > 100 {
		now := time.Now()
		for k, v := range s.locks {
			if now.Sub(v.lastUsed) > lockEvictAge {
				delete(s.locks, k)
			}
		}
	}

	lock, ok := s.locks[id]
	if !ok {
		lock = &sessionLock{}
		s.locks[id] = lock
	}
	lock.lastUsed = time.Now()
	return lock
}

func (s *FileStore) Get(ctx context.Context, id string) (Session, bool, error) {
	select {
	case <-ctx.Done():
		return Session{}, false, ctx.Err()
	default:
	}
	lock := s.lockFor(id)
	lock.Lock()
	defer lock.Unlock()

	data, err := os.ReadFile(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return Session{}, false, err
	}
	return sess, true, nil
}

func (s *FileStore) Save(ctx context.Context, sess Session) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	lock := s.lockFor(sess.ID)
	lock.Lock()
	defer lock.Unlock()

	now := time.Now().UTC()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	sess.UpdatedAt = now

	data, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	tmp := s.path(sess.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(sess.ID))
}

func (s *FileStore) path(id string) string {
	id = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
	return filepath.Join(s.dir, id+".json")
}

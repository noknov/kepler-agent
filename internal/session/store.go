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
	Summary          string        `json:"summary,omitempty"`
	Turns            []memory.Turn `json:"turns,omitempty"`
	PendingUserInput bool          `json:"pending_user_input,omitempty"`
	PendingUserID    string        `json:"pending_user_id,omitempty"`
	PendingQuestion  string        `json:"pending_question,omitempty"`
	PendingActionKey string        `json:"pending_action_key,omitempty"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

type Store interface {
	Get(ctx context.Context, id string) (Session, bool, error)
	Save(ctx context.Context, s Session) error
}

type FileStore struct {
	dir string
	mu  sync.Mutex
}

func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &FileStore{dir: dir}, nil
}

func ID(channel, threadTS string) string {
	raw := channel + ":" + threadTS
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func (s *FileStore) Get(ctx context.Context, id string) (Session, bool, error) {
	select {
	case <-ctx.Done():
		return Session{}, false, ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()

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
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	sess.UpdatedAt = now

	data, err := json.MarshalIndent(sess, "", "  ")
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

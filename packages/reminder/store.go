package reminder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Reminder is a durable, one-time Slack reminder.
type Reminder struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Channel   string    `json:"channel"`
	ThreadTS  string    `json:"thread_ts"`
	Message   string    `json:"message"`
	RunAt     time.Time `json:"run_at"`
	CreatedAt time.Time `json:"created_at"`
	SentAt    time.Time `json:"sent_at,omitempty"`
}

type Store interface {
	Create(context.Context, Reminder) (Reminder, error)
	List(context.Context, string) ([]Reminder, error)
	Due(context.Context, time.Time) ([]Reminder, error)
	MarkSent(context.Context, string, time.Time) error
	Cancel(context.Context, string, string) error
}

// FileStore keeps reminders in one atomic JSON file. It is intentionally small
// and dependency-free; a database store can implement Store later without
// changing the tool or scheduler.
type FileStore struct {
	path string
	mu   sync.Mutex
}

func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &FileStore{path: filepath.Join(dir, "reminders.json")}, nil
}

func (s *FileStore) Create(ctx context.Context, r Reminder) (Reminder, error) {
	if err := ctx.Err(); err != nil {
		return Reminder{}, err
	}
	if r.ID == "" {
		return Reminder{}, fmt.Errorf("reminder id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return Reminder{}, err
	}
	for _, existing := range all {
		if existing.ID == r.ID {
			return Reminder{}, fmt.Errorf("reminder id already exists")
		}
	}
	r.CreatedAt = time.Now().UTC()
	all = append(all, r)
	return r, s.save(all)
}

func (s *FileStore) List(ctx context.Context, userID string) ([]Reminder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Reminder, 0, len(all))
	for _, r := range all {
		if r.UserID == userID && r.SentAt.IsZero() {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RunAt.Before(out[j].RunAt) })
	return out, nil
}

func (s *FileStore) Due(ctx context.Context, now time.Time) ([]Reminder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	var out []Reminder
	for _, r := range all {
		if r.SentAt.IsZero() && !r.RunAt.After(now) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *FileStore) MarkSent(ctx context.Context, id string, sentAt time.Time) error {
	return s.update(ctx, id, "", func(r *Reminder) { r.SentAt = sentAt.UTC() })
}
func (s *FileStore) Cancel(ctx context.Context, id, userID string) error {
	return s.update(ctx, id, userID, func(r *Reminder) { r.SentAt = time.Now().UTC() })
}

func (s *FileStore) update(ctx context.Context, id, userID string, change func(*Reminder)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	for i := range all {
		if all[i].ID == id && (userID == "" || all[i].UserID == userID) && all[i].SentAt.IsZero() {
			change(&all[i])
			return s.save(all)
		}
	}
	return fmt.Errorf("reminder not found")
}

func (s *FileStore) load() ([]Reminder, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all []Reminder
	return all, json.Unmarshal(data, &all)
}
func (s *FileStore) save(all []Reminder) error {
	data, err := json.Marshal(all)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

package runs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
)

type Run struct {
	ID               string        `json:"id"`
	SessionID        string        `json:"session_id"`
	EventID          string        `json:"event_id,omitempty"`
	UserID           string        `json:"user_id,omitempty"`
	Channel          string        `json:"channel,omitempty"`
	ThreadTS         string        `json:"thread_ts,omitempty"`
	Provider         string        `json:"provider,omitempty"`
	Model            string        `json:"model,omitempty"`
	Status           string        `json:"status"`
	StartedAt        time.Time     `json:"started_at"`
	EndedAt          time.Time     `json:"ended_at,omitempty"`
	DurationMS       int64         `json:"duration_ms,omitempty"`
	Usage            llm.Usage     `json:"usage"`
	EstimatedCostUSD float64       `json:"estimated_cost_usd,omitempty"`
	ErrorID          string        `json:"error_id,omitempty"`
	Error            string        `json:"error,omitempty"`
	FinalHash        string        `json:"final_hash,omitempty"`
	Steps            []Step        `json:"steps,omitempty"`
	Feedback         []Feedback    `json:"feedback,omitempty"`
	Quality          *QualityScore `json:"quality,omitempty"`
}

type Step struct {
	ID               string         `json:"id"`
	Type             string         `json:"type"`
	Name             string         `json:"name,omitempty"`
	StartedAt        time.Time      `json:"started_at"`
	DurationMS       int64          `json:"duration_ms"`
	Usage            llm.Usage      `json:"usage,omitempty"`
	EstimatedCostUSD float64        `json:"estimated_cost_usd,omitempty"`
	Error            string         `json:"error,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type Feedback struct {
	Source    string    `json:"source"`
	Value     string    `json:"value"`
	UserID    string    `json:"user_id,omitempty"`
	Channel   string    `json:"channel,omitempty"`
	MessageTS string    `json:"message_ts,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type QualityScore struct {
	Automatic float64   `json:"automatic,omitempty"`
	Manual    float64   `json:"manual,omitempty"`
	Notes     []string  `json:"notes,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store interface {
	Save(ctx context.Context, run Run) error
	Get(ctx context.Context, id string) (Run, bool, error)
	AddFeedback(ctx context.Context, runID string, feedback Feedback) error
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

func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "run-" + time.Now().UTC().Format("20060102150405")
	}
	return "run-" + hex.EncodeToString(b[:])
}

func HashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:8])
}

func (s *FileStore) Save(ctx context.Context, run Run) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(run)
}

func (s *FileStore) Get(ctx context.Context, id string) (Run, bool, error) {
	select {
	case <-ctx.Done():
		return Run{}, false, ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return Run{}, false, err
	}
	return run, true, nil
}

func (s *FileStore) AddFeedback(ctx context.Context, runID string, feedback Feedback) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path(runID))
	if err != nil {
		return err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return err
	}
	run.Feedback = append(run.Feedback, feedback)
	run.Quality = scoreFeedback(run.Feedback)
	return s.saveLocked(run)
}

func (s *FileStore) saveLocked(run Run) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(run.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(run.ID))
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

func scoreFeedback(feedback []Feedback) *QualityScore {
	if len(feedback) == 0 {
		return nil
	}
	var sum float64
	var count int
	notes := make([]string, 0, len(feedback))
	for _, fb := range feedback {
		score, ok := reactionScore(fb.Value)
		if !ok {
			continue
		}
		sum += score
		count++
		notes = append(notes, fb.Source+":"+fb.Value)
	}
	if count == 0 {
		return nil
	}
	return &QualityScore{
		Manual:    sum / float64(count),
		Notes:     notes,
		UpdatedAt: time.Now().UTC(),
	}
}

func reactionScore(value string) (float64, bool) {
	switch strings.ToLower(strings.Trim(value, ": ")) {
	case "+1", "thumbsup", "white_check_mark", "heavy_check_mark":
		return 1, true
	case "-1", "thumbsdown", "x":
		return 0, true
	case "eyes", "thinking_face", "question":
		return 0.5, true
	default:
		return 0, false
	}
}

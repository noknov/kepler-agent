package runs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/llm"
)

type Run struct {
	ID               string        `json:"id"`
	TraceID          string        `json:"trace_id,omitempty"`
	SessionID        string        `json:"session_id"`
	EventID          string        `json:"event_id,omitempty"`
	UserID           string        `json:"user_id,omitempty"`
	Channel          string        `json:"channel,omitempty"`
	ThreadTS         string        `json:"thread_ts,omitempty"`
	SlackMessageTS   string        `json:"slack_message_ts,omitempty"`
	SlackChannel     string        `json:"slack_channel,omitempty"`
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
	ErrorStack       string        `json:"error_stack,omitempty"`
	FinalHash        string        `json:"final_hash,omitempty"`
	Steps            []Step        `json:"steps,omitempty"`
	Feedback         []Feedback    `json:"feedback,omitempty"`
	Quality          *QualityScore `json:"quality,omitempty"`
}

type Step struct {
	ID               string         `json:"id"`
	SpanID           string         `json:"span_id,omitempty"`
	ParentSpanID     string         `json:"parent_span_id,omitempty"`
	Type             string         `json:"type"`
	Name             string         `json:"name,omitempty"`
	StartedAt        time.Time      `json:"started_at"`
	DurationMS       int64          `json:"duration_ms"`
	Usage            llm.Usage      `json:"usage,omitempty"`
	EstimatedCostUSD float64        `json:"estimated_cost_usd,omitempty"`
	Error            string         `json:"error,omitempty"`
	FinishReason     string         `json:"finish_reason,omitempty"`
	Content          string         `json:"content,omitempty"`
	ToolCallNames    []string       `json:"tool_call_names,omitempty"`
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
	List(ctx context.Context, limit int) ([]Run, error)
	// ListBySession returns all runs for the given session, ordered newest first.
	// Implementations must avoid full-scan when possible; callers should prefer
	// this over List when a sessionID filter is needed.
	ListBySession(ctx context.Context, sessionID string) ([]Run, error)
	AddFeedback(ctx context.Context, runID string, feedback Feedback) error
	AddFeedbackForMessage(ctx context.Context, channel, messageTS string, feedback Feedback) (string, bool, error)
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

func NewTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "trace-" + time.Now().UTC().Format("20060102150405")
	}
	return "trace-" + hex.EncodeToString(b[:])
}

func NewSpanID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "span-" + time.Now().UTC().Format("150405")
	}
	return "span-" + hex.EncodeToString(b[:])
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

func (s *FileStore) AddFeedbackForMessage(ctx context.Context, channel, messageTS string, feedback Feedback) (string, bool, error) {
	select {
	case <-ctx.Done():
		return "", false, ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return "", false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			return "", false, err
		}
		var run Run
		if err := json.Unmarshal(data, &run); err != nil {
			continue
		}
		if run.SlackChannel == channel && run.SlackMessageTS == messageTS {
			run.Feedback = append(run.Feedback, feedback)
			run.Quality = scoreRun(run)
			if err := s.saveLocked(run); err != nil {
				return "", false, err
			}
			return run.ID, true, nil
		}
	}
	return "", false, nil
}

func (s *FileStore) List(ctx context.Context, limit int) ([]Run, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		var run Run
		if err := json.Unmarshal(data, &run); err != nil {
			continue
		}
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *FileStore) ListBySession(ctx context.Context, sessionID string) ([]Run, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if sessionID == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []Run
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		// Fast path: skip JSON decode if session ID is not in the raw bytes.
		// session_id values are always plain ASCII, so a bytes search is safe.
		if !bytes.Contains(data, []byte(sessionID)) {
			continue
		}
		var run Run
		if err := json.Unmarshal(data, &run); err != nil {
			continue
		}
		if run.SessionID != sessionID {
			continue
		}
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
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

func scoreRun(run Run) *QualityScore {
	score := 0.75
	notes := []string{}
	switch run.Status {
	case "completed":
		score += 0.15
	case "pending_user":
		score += 0.05
		notes = append(notes, "pending_user")
	case "error":
		score -= 0.45
		notes = append(notes, "run_error")
	}
	toolSteps := 0
	for _, step := range run.Steps {
		if step.Error != "" {
			score -= 0.1
			notes = append(notes, "step_error:"+step.Name)
		}
		if step.Type == "tool" {
			toolSteps++
		}
	}
	if toolSteps > 0 {
		score += 0.1
		notes = append(notes, "tool_evidence")
	}
	if run.FinalHash == "" && run.Status == "completed" {
		score -= 0.15
		notes = append(notes, "missing_final")
	}
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	quality := &QualityScore{Automatic: score, Notes: notes, UpdatedAt: time.Now().UTC()}
	if manual := scoreFeedback(run.Feedback); manual != nil {
		quality.Manual = manual.Manual
		quality.Notes = append(quality.Notes, manual.Notes...)
	}
	return quality
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

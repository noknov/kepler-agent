package runs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
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
	Termination      string        `json:"termination,omitempty"`
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

type UserAuditSummary struct {
	UserID           string
	Requests         int
	Conversations    int
	Completed        int
	Failed           int
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	EstimatedCostUSD float64
	FirstStartedAt   time.Time
	LastStartedAt    time.Time
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
	Automatic float64        `json:"automatic,omitempty"`
	Manual    float64        `json:"manual,omitempty"`
	Signals   map[string]int `json:"signals,omitempty"`
	Notes     []string       `json:"notes,omitempty"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type Store interface {
	Save(ctx context.Context, run Run) error
	Get(ctx context.Context, id string) (Run, bool, error)
	List(ctx context.Context, limit int) ([]Run, error)
	// ListBySession returns all runs for the given session, ordered newest first.
	// Implementations must avoid full-scan when possible; callers should prefer
	// this over List when a sessionID filter is needed.
	ListBySession(ctx context.Context, sessionID string) ([]Run, error)
	UserAuditSummaries(ctx context.Context, start, end time.Time) ([]UserAuditSummary, error)
	AddFeedback(ctx context.Context, runID string, feedback Feedback) error
	AddFeedbackForMessage(ctx context.Context, channel, messageTS string, feedback Feedback) (string, bool, error)
}

// StepStore is implemented by stores that persist run items append-only. It
// avoids rewriting an ever-growing Run document after every agent step.
type StepStore interface {
	AppendStep(ctx context.Context, runID string, step Step) error
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
	signals := map[string]int{}
	switch run.Status {
	case "completed":
		score += 0.15
		signals["completed"]++
	case "pending_user":
		score += 0.05
		signals["pending_user"]++
		notes = append(notes, "pending_user")
	case "error":
		score -= 0.45
		signals["run_error"]++
		notes = append(notes, "run_error")
	case "incomplete":
		score -= 0.2
		signals["incomplete"]++
		notes = append(notes, "incomplete:"+run.Termination)
	}
	toolSteps := 0
	llmSteps := 0
	retries := 0
	for _, step := range run.Steps {
		if step.Error != "" {
			score -= 0.1
			signals["step_error"]++
			notes = append(notes, "step_error:"+step.Name)
		}
		if step.Type == "tool" {
			toolSteps++
		}
		if step.Type == "llm" {
			llmSteps++
			if step.FinishReason == "max_tokens" || step.FinishReason == "length" {
				score -= 0.05
				signals["llm_length_stop"]++
			}
		}
		if step.Type == "event" {
			switch step.Name {
			case "max_output_tokens_recovery", "compact_error", "context_compact", "aggregate_budget_applied", "stateful_aggregate_budget_applied":
				signals[step.Name]++
			}
			if strings.Contains(step.Name, "retry") || strings.Contains(step.Name, "recovery") {
				retries++
			}
			if step.Name == "tool_policy_decision" {
				if allowed, _ := step.Metadata["allowed"].(bool); !allowed {
					score -= 0.08
					signals["tool_policy_denied"]++
				}
			}
			if step.Name == "termination" {
				if reason, _ := step.Metadata["reason"].(string); reason != "" && reason != "completed" {
					score -= 0.08
					signals["termination_"+reason]++
					notes = append(notes, "termination:"+reason)
				}
			}
		}
	}
	if toolSteps > 0 {
		score += 0.1
		signals["tool_evidence"] = toolSteps
		notes = append(notes, "tool_evidence")
	}
	if llmSteps > 4 {
		score -= 0.03
		signals["many_llm_steps"]++
	}
	if retries > 0 {
		score -= 0.03 * float64(retries)
		signals["retries"] = retries
	}
	if run.FinalHash == "" && run.Status == "completed" {
		score -= 0.15
		signals["missing_final"]++
		notes = append(notes, "missing_final")
	}
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	quality := &QualityScore{Automatic: score, Signals: signals, Notes: notes, UpdatedAt: time.Now().UTC()}
	if manual := scoreFeedback(run.Feedback); manual != nil {
		quality.Manual = manual.Manual
		quality.Notes = append(quality.Notes, manual.Notes...)
	}
	return quality
}

// Score derives the stable automatic quality projection for a completed run.
func Score(run Run) *QualityScore { return scoreRun(run) }

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

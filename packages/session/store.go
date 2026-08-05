package session

import (
	"context"
	"encoding/base64"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/memory"
)

type Session struct {
	ID                  string                            `json:"id"`
	Channel             string                            `json:"channel"`
	ThreadTS            string                            `json:"thread_ts"`
	UserID              string                            `json:"user_id"`
	Locale              string                            `json:"locale,omitempty"`
	Summary             string                            `json:"summary,omitempty"`
	Turns               []memory.Turn                     `json:"turns,omitempty"`
	CompactBoundaries   []memory.CompactBoundary          `json:"compact_boundaries,omitempty"`
	ContentReplacements []memory.ContentReplacementRecord `json:"content_replacements,omitempty"`
	PendingUserInput    bool                              `json:"pending_user_input,omitempty"`
	PendingUserID       string                            `json:"pending_user_id,omitempty"`
	PendingQuestion     string                            `json:"pending_question,omitempty"`
	CreatedAt           time.Time                         `json:"created_at"`
	UpdatedAt           time.Time                         `json:"updated_at"`
}

type Store interface {
	Get(ctx context.Context, id string) (Session, bool, error)
	Save(ctx context.Context, s Session) error
}

// Locker is implemented by durable stores that can serialize an entire turn.
// The PostgreSQL implementation uses advisory locks, which work across pods.
type Locker interface {
	Lock(ctx context.Context, id string) (unlock func(), err error)
}

func ID(channel, threadTS string) string {
	raw := channel + ":" + threadTS
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

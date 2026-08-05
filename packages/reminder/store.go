package reminder

import (
	"context"
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

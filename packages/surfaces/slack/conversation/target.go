package slackconversation

import (
	"fmt"
	"github.com/noknov/kepler-agent/packages/session"
)

// The empty kind preserves existing durable thread requests. A code channel
// must be selected explicitly from verified Slack metadata, never its name or
// the absence of a thread timestamp.
type SessionKind string

const (
	ThreadSession      SessionKind = ""
	CodeChannelSession SessionKind = "code_channel"
)

type Target struct {
	Kind     SessionKind
	Channel  string
	ThreadTS string
}

func (t Target) Validate() error {
	if t.Channel == "" {
		return fmt.Errorf("Slack session requires a channel")
	}
	switch t.Kind {
	case ThreadSession:
		if t.ThreadTS == "" {
			return fmt.Errorf("Slack thread session requires a parent timestamp")
		}
	case CodeChannelSession:
		if t.ThreadTS != "" {
			return fmt.Errorf("Slack code channel cannot have a thread timestamp")
		}
	default:
		return fmt.Errorf("unknown Slack session kind")
	}
	return nil
}

func (t Target) SessionID() string { return session.ID(t.Channel, t.ThreadTS) }
func (r Request) Target() Target {
	return Target{Kind: r.SessionKind, Channel: r.Channel, ThreadTS: r.ThreadTS}
}
func (r StopRequest) Target() Target {
	return Target{Kind: r.SessionKind, Channel: r.Channel, ThreadTS: r.ThreadTS}
}

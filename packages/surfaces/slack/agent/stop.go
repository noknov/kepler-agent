package slackagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/session"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	"github.com/redis/go-redis/v9"
)

// A stop targets one turn, so a delayed/replayed button event cannot cancel
// the next turn in the same thread. PostgreSQL owns delivery; Redis only wakes
// the worker that currently holds the session.
type stopCommand struct {
	TurnID string `json:"turn_id"`
}

const sessionStopChannel = "agent:stop"

func (s *Service) StopSession(ctx context.Context, req conversation.StopRequest) error {
	if req.EventID == "" || req.Channel == "" || req.ThreadTS == "" || s.Inputs == nil {
		return fmt.Errorf("stop requires an event, thread and durable input store")
	}
	clickedAt, err := parseSlackEventTime(req.EventTS)
	if err != nil {
		return fmt.Errorf("stop requires a valid Slack event timestamp")
	}
	sessionID := session.ID(req.Channel, req.ThreadTS)
	s.mu.Lock()
	active := s.active[sessionID]
	s.mu.Unlock()
	var events []transcript.Event
	if s.Agent.Runtime != nil {
		events, err = s.Agent.Runtime.SessionEvents(ctx, sessionID)
		if err != nil {
			return err
		}
	}
	turnID, stale := stopTarget(events, active, clickedAt)
	if stale {
		return nil
	}
	owner := ""
	local := active != nil && active.turnID == turnID
	if s.Redis != nil && !local {
		owner, err = s.Redis.Get(ctx, "agent:active:"+sessionID)
		if err != nil && !errors.Is(err, redis.Nil) {
			if turnID == "" {
				return err // cannot establish whether another worker is starting
			}
			log.Printf("slack stop wake-up routing unavailable session=%s error_type=%T", sessionID, err)
		}
	}
	if turnID == "" {
		if owner != "" {
			// Another worker registered the run but has not persisted TurnStarted
			// yet. Leave the inbox event retryable instead of losing the stop.
			return fmt.Errorf("session is starting; retry stop after turn registration")
		}
		return s.clearStoppedSession(ctx, req)
	}
	payload, err := json.Marshal(stopCommand{TurnID: turnID})
	if err != nil {
		return err
	}
	if err := s.Inputs.Enqueue(ctx, sessioninput.Item{ID: req.EventID, SessionID: sessionID, Kind: sessioninput.KindStop, Payload: payload}); err != nil {
		return err
	}
	if s.Redis != nil {
		// Wake every worker: the owner hint can point to a locally registered
		// waiter rather than the runtime that currently holds the PG lock.
		_, _ = s.Redis.PublishCount(ctx, sessionStopChannel, sessionID)
	}
	if local || owner == "" || owner == s.PodID {
		if err := s.consumeStops(ctx, sessionID); err != nil {
			return err
		}
		if active == nil && owner == "" {
			// The process that owned an unfinished durable turn is gone. There
			// is no cancellation defer left to clear its stale loading state.
			return s.clearStoppedSession(ctx, req)
		}
		return nil
	}
	return nil
}

func (s *Service) handleStopSignal(ctx context.Context, sessionID string) {
	s.mu.Lock()
	active := s.active[sessionID]
	s.mu.Unlock()
	if active == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.consumeStops(ctx, sessionID); err != nil {
		log.Printf("slack stop command failed session=%s: %v", sessionID, err)
	}
}

func (s *Service) pollSessionStops(ctx context.Context) {
	s.mu.Lock()
	sessions := make([]string, 0, len(s.active))
	for sessionID := range s.active {
		sessions = append(sessions, sessionID)
	}
	s.mu.Unlock()
	for _, sessionID := range sessions {
		go s.handleStopSignal(ctx, sessionID)
	}
}

func (s *Service) clearStoppedSession(ctx context.Context, req conversation.StopRequest) error {
	if messenger, ok := s.Messenger.(conversation.AgentSessionMessenger); ok {
		return messenger.SetAgentSessionStatus(ctx, req.Channel, req.ThreadTS, req.UserID, sessionActive)
	}
	return nil
}

// The latest durable start identifies the lock-holding runtime, which may be
// different from a local request still waiting to acquire the session lock.
func latestStopTurn(events []transcript.Event) (transcript.Event, bool) {
	ended := make(map[string]bool)
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type.IsTurnTerminal() {
			ended[event.TurnID] = true
		}
		if event.Type == transcript.TurnStarted {
			return event, ended[event.TurnID]
		}
	}
	return transcript.Event{}, false
}

func stopTarget(events []transcript.Event, active *activeRun, clickedAt time.Time) (string, bool) {
	latest, ended := latestStopTurn(events)
	if active != nil && (latest.TurnID == "" || latest.TurnID == active.turnID || ended) {
		if active.startedAt.After(clickedAt) {
			return "", true
		}
		return active.turnID, false
	}
	if latest.Timestamp.After(clickedAt) {
		return "", true
	}
	if ended {
		return "", false
	}
	return latest.TurnID, false
}

func parseSlackEventTime(value string) (time.Time, error) {
	seconds, fraction, _ := strings.Cut(value, ".")
	sec, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil || sec <= 0 || len(fraction) > 9 {
		return time.Time{}, fmt.Errorf("invalid Slack timestamp")
	}
	for _, digit := range fraction {
		if digit < '0' || digit > '9' {
			return time.Time{}, fmt.Errorf("invalid Slack timestamp fraction")
		}
	}
	ns, err := strconv.ParseInt(fraction+strings.Repeat("0", 9-len(fraction)), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(sec, ns), nil
}

func (s *Service) consumeStops(ctx context.Context, sessionID string) error {
	if s.Inputs == nil {
		return nil
	}
	s.mu.Lock()
	active := s.active[sessionID]
	s.mu.Unlock()
	if s.Agent.Runtime != nil {
		events, err := s.Agent.Runtime.SessionEvents(ctx, sessionID)
		if err != nil {
			return err
		}
		latest, ended := latestStopTurn(events)
		if latest.TurnID != "" && !ended && (active == nil || latest.TurnID != active.turnID) {
			return nil // another runtime still owns this turn; do not claim its stop
		}
	}
	if active == nil && s.Redis != nil {
		owner, err := s.Redis.Get(ctx, "agent:active:"+sessionID)
		if err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
		if owner != "" && owner != s.PodID {
			return nil
		}
	}
	owner := s.PodID
	if owner == "" {
		owner = "local-slack-service"
	}
	items, err := s.Inputs.Claim(ctx, sessionID, sessioninput.KindStop, owner, time.Minute, 100)
	if err != nil {
		return err
	}
	for _, item := range items {
		var command stopCommand
		if err := json.Unmarshal(item.Payload, &command); err != nil {
			_ = s.Inputs.Release(ctx, item.ID, owner)
			return err
		}
		s.mu.Lock()
		active = s.active[sessionID]
		s.mu.Unlock()
		if active != nil && active.turnID == command.TurnID {
			active.cancel()
		} else if active != nil {
			// A different locally registered request may still be waiting for
			// the PG session lock. It must not consume the actual owner's stop.
			finished := false
			if s.Agent.Runtime != nil {
				events, err := s.Agent.Runtime.SessionEvents(ctx, sessionID)
				if err != nil {
					_ = s.Inputs.Release(ctx, item.ID, owner)
					return err
				}
				latest, ended := latestStopTurn(events)
				finished = latest.TurnID != "" && (ended || latest.TurnID != command.TurnID)
			}
			if !finished {
				if err := s.Inputs.Release(ctx, item.ID, owner); err != nil {
					return err
				}
				continue
			}
		}
		if err := s.Inputs.Ack(ctx, item.ID, owner); err != nil {
			return err
		}
	}
	return nil
}

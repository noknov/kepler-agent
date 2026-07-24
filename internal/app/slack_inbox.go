package app

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/noknov/slack-copilot-agent/internal/slack"
)

const (
	redisEventChannel  = "slack_event_inbox:new"
	fallbackPollPeriod = 60 * time.Second
)

func (s *Server) startEventWorkers(ctx context.Context) {
	if err := s.eventInbox.RecoverExpired(ctx); err != nil {
		log.Printf("recover expired Slack inbox events: %v", err)
	}
	s.replayQueuedEvents(ctx)
	workers := s.eventWorkers
	if workers <= 0 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		s.Go(func(ctx context.Context) {
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-s.eventQueue:
					s.handleQueuedSlackEvent(ctx, job)
				}
			}
		})
	}

	s.Go(func(ctx context.Context) {
		s.subscribeRedisEvents(ctx)
	})

	s.Go(func(ctx context.Context) {
		ticker := time.NewTicker(fallbackPollPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s.draining.Load() {
					return
				}
				if err := s.eventInbox.RecoverExpired(ctx); err != nil {
					log.Printf("recover expired Slack inbox events: %v", err)
				}
				s.replayQueuedEvents(ctx)
			}
		}
	})
}

// subscribeRedisEvents listens to Redis pub/sub for new event notifications,
// replacing the old 1-second polling loop with event-driven processing.
func (s *Server) subscribeRedisEvents(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		sub := s.redis.Subscribe(ctx, redisEventChannel)
		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				_ = sub.Close()
				return
			case _, ok := <-ch:
				if !ok {
					_ = sub.Close()
					goto reconnect
				}
				if s.draining.Load() {
					_ = sub.Close()
					return
				}
				s.replayQueuedEvents(ctx)
			}
		}
	reconnect:
		log.Printf("redis pub/sub disconnected, reconnecting in 2s")
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Server) handleQueuedSlackEvent(ctx context.Context, job slackEventJob) {
	if !s.beginEvent() {
		return
	}
	defer s.endEvent()
	claimed, err := s.eventInbox.Start(ctx, job.eventID, s.eventInboxLease)
	if err != nil {
		log.Printf("claim Slack inbox event %s: %v", job.eventID, err)
		return
	}
	if !claimed {
		return
	}
	eventCtx, cancel := context.WithTimeout(ctx, s.eventTimeout)
	err = s.handleEvent(eventCtx, job.eventID, job.event)
	cancel()
	if err != nil {
		log.Printf("handle Slack inbox event %s: %v", job.eventID, err)
		if requeueErr := s.eventInbox.Requeue(context.Background(), job.eventID); requeueErr != nil {
			log.Printf("requeue Slack inbox event %s: %v", job.eventID, requeueErr)
		}
		return
	}
	if err := s.eventInbox.Complete(context.Background(), job.eventID); err != nil {
		log.Printf("complete Slack inbox event %s: %v", job.eventID, err)
	}
}

// replayQueuedEvents keeps the database inbox as the source of truth when the
// in-memory worker queue is full. Worker-side Start makes replay idempotent.
func (s *Server) replayQueuedEvents(ctx context.Context) {
	if s.draining.Load() {
		return
	}
	pending, err := s.eventInbox.Pending(ctx, cap(s.eventQueue))
	if err != nil {
		log.Printf("load durable Slack inbox: %v", err)
		return
	}
	for _, item := range pending {
		var event slack.Event
		if err := json.Unmarshal(item.Payload, &event); err != nil {
			log.Printf("decode queued Slack event %s: %v", item.ID, err)
			continue
		}
		select {
		case s.eventQueue <- slackEventJob{eventID: item.ID, event: event}:
		default:
			return
		}
	}
}

func (s *Server) enqueueSlackEvent(ctx context.Context, eventID string, event slack.Event) bool {
	if s.draining.Load() {
		return false
	}
	if timeout := s.eventEnqueueTimeout; timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	select {
	case s.eventQueue <- slackEventJob{eventID: eventID, event: event}:
		_ = s.redis.Publish(ctx, redisEventChannel, eventID)
		return true
	case <-ctx.Done():
		return false
	}
}

package slackevents

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/eventinbox"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/slack"
)

const (
	RedisEventChannel  = "slack_event_inbox:new"
	FallbackPollPeriod = 60 * time.Second
)

type Handler func(context.Context, string, slack.Event) error

type Worker struct {
	Inbox          *eventinbox.PGStore
	Redis          *redisclient.Client
	Handler        Handler
	Workers        int
	QueueSize      int
	EventTimeout   time.Duration
	InboxLease     time.Duration
	IsDraining     func() bool
	BeginEvent     func() bool
	EndEvent       func()
	StartGoroutine func(func(context.Context))

	queue chan job
}

type job struct {
	eventID string
	event   slack.Event
}

func (w *Worker) Start(ctx context.Context) {
	if w == nil || w.Inbox == nil || w.Handler == nil {
		return
	}
	if w.queue == nil {
		size := w.QueueSize
		if size <= 0 {
			size = 512
		}
		w.queue = make(chan job, size)
	}
	if err := w.Inbox.RecoverExpired(ctx); err != nil {
		log.Printf("recover expired Slack inbox events: %v", err)
	}
	w.replay(ctx)
	workers := w.Workers
	if workers <= 0 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		w.goRun(func(ctx context.Context) {
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-w.queue:
					w.handle(ctx, job)
				}
			}
		})
	}
	w.goRun(w.subscribeRedisEvents)
	w.goRun(func(ctx context.Context) {
		ticker := time.NewTicker(FallbackPollPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if w.draining() {
					return
				}
				if err := w.Inbox.RecoverExpired(ctx); err != nil {
					log.Printf("recover expired Slack inbox events: %v", err)
				}
				w.replay(ctx)
			}
		}
	})
}

func (w *Worker) Notify(ctx context.Context, eventID string, event slack.Event, timeout time.Duration) bool {
	if w == nil || w.draining() {
		return false
	}
	if w.queue == nil {
		size := w.QueueSize
		if size <= 0 {
			size = 512
		}
		w.queue = make(chan job, size)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	select {
	case w.queue <- job{eventID: eventID, event: event}:
		if w.Redis != nil {
			_ = w.Redis.Publish(ctx, RedisEventChannel, eventID)
		}
		return true
	case <-ctx.Done():
		return false
	}
}

func (w *Worker) Publish(ctx context.Context, eventID string) {
	if w != nil && w.Redis != nil {
		_ = w.Redis.Publish(ctx, RedisEventChannel, eventID)
	}
}

func (w *Worker) subscribeRedisEvents(ctx context.Context) {
	if w.Redis == nil {
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		sub := w.Redis.Subscribe(ctx, RedisEventChannel)
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
				if w.draining() {
					_ = sub.Close()
					return
				}
				w.replay(ctx)
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

func (w *Worker) handle(ctx context.Context, job job) {
	if !w.begin() {
		return
	}
	defer w.end()
	claimed, err := w.Inbox.Start(ctx, job.eventID, w.InboxLease)
	if err != nil {
		log.Printf("claim Slack inbox event %s: %v", job.eventID, err)
		return
	}
	if !claimed {
		return
	}
	eventCtx, cancel := context.WithTimeout(ctx, w.EventTimeout)
	err = w.Handler(eventCtx, job.eventID, job.event)
	cancel()
	if err != nil {
		log.Printf("handle Slack inbox event %s: %v", job.eventID, err)
		if requeueErr := w.Inbox.Requeue(context.Background(), job.eventID); requeueErr != nil {
			log.Printf("requeue Slack inbox event %s: %v", job.eventID, requeueErr)
		}
		return
	}
	if err := w.Inbox.Complete(context.Background(), job.eventID); err != nil {
		log.Printf("complete Slack inbox event %s: %v", job.eventID, err)
	}
}

func (w *Worker) replay(ctx context.Context) {
	if w.draining() {
		return
	}
	pending, err := w.Inbox.Pending(ctx, cap(w.queue))
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
		case w.queue <- job{eventID: item.ID, event: event}:
		default:
			return
		}
	}
}

func (w *Worker) goRun(fn func(context.Context)) {
	if w.StartGoroutine != nil {
		w.StartGoroutine(fn)
		return
	}
	go fn(context.Background())
}

func (w *Worker) draining() bool {
	return w.IsDraining != nil && w.IsDraining()
}

func (w *Worker) begin() bool {
	if w.BeginEvent != nil {
		return w.BeginEvent()
	}
	return true
}

func (w *Worker) end() {
	if w.EndEvent != nil {
		w.EndEvent()
	}
}

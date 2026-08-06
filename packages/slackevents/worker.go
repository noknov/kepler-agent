package slackevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
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

type Observer interface {
	EventInboxQueue(depth, capacity int)
	EventInboxJob(result string)
}

type Worker struct {
	Inbox          eventinbox.Store
	Redis          *redisclient.Client
	Handler        Handler
	Workers        int
	QueueSize      int
	EventTimeout   time.Duration
	InboxLease     time.Duration
	MaxAttempts    int
	RetryBase      time.Duration
	RetryMax       time.Duration
	IsDraining     func() bool
	BeginEvent     func() bool
	EndEvent       func()
	StartGoroutine func(func(context.Context))
	Observer       Observer

	queue chan job
}

type job struct {
	eventID string
	event   slack.Event
	attempt int
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
	if err := w.Inbox.RecoverExpired(ctx, w.maxAttempts()); err != nil {
		log.Printf("recover expired Slack inbox events: %v", err)
	}
	w.replay(ctx)
	workers := w.Workers
	if workers <= 0 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		w.goRun(ctx, func(ctx context.Context) {
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
	w.goRun(ctx, w.subscribeRedisEvents)
	w.goRun(ctx, func(ctx context.Context) {
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
				if err := w.Inbox.RecoverExpired(ctx, w.maxAttempts()); err != nil {
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
		w.observeQueue()
		if w.Redis != nil {
			_ = w.Redis.Publish(ctx, RedisEventChannel, eventID)
		}
		return true
	case <-ctx.Done():
		w.observeJob("notify_timeout")
		return false
	default:
		w.observeQueue()
		w.observeJob("queue_full")
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
		w.observeJob("begin_rejected")
		return
	}
	defer w.end()
	claimed, err := w.Inbox.Start(ctx, job.eventID, w.InboxLease)
	if err != nil {
		log.Printf("claim Slack inbox event %s: %v", job.eventID, err)
		w.observeJob("claim_error")
		return
	}
	if !claimed {
		w.observeJob("claim_skipped")
		return
	}
	eventCtx, cancel := context.WithTimeout(ctx, w.EventTimeout)
	heartbeatDone := make(chan struct{})
	go w.renewLease(eventCtx, job.eventID, heartbeatDone)
	err = w.Handler(eventCtx, job.eventID, job.event)
	cancel()
	<-heartbeatDone
	if err != nil {
		log.Printf("handle Slack inbox event %s: %v", job.eventID, err)
		finalCtx, finalCancel := context.WithTimeout(context.Background(), 5*time.Second)
		dead, failErr := w.Inbox.Fail(finalCtx, job.eventID, err, w.retryDelay(job.eventID, job.attempt+1), w.maxAttempts())
		finalCancel()
		if failErr != nil && !errors.Is(failErr, eventinbox.ErrLeaseLost) {
			log.Printf("fail Slack inbox event %s: %v", job.eventID, failErr)
		} else if dead {
			log.Printf("dead-lettered Slack inbox event %s after %d attempts", job.eventID, w.maxAttempts())
			w.observeJob("dead_letter")
		}
		w.observeJob("handler_error")
		return
	}
	finalCtx, finalCancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = w.Inbox.Complete(finalCtx, job.eventID)
	finalCancel()
	if err != nil {
		log.Printf("complete Slack inbox event %s: %v", job.eventID, err)
		w.observeJob("complete_error")
		return
	}
	w.observeJob("completed")
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
			if deadErr := w.Inbox.DeadLetter(ctx, item.ID, fmt.Errorf("decode event payload: %w", err)); deadErr != nil {
				log.Printf("dead-letter malformed Slack event %s: %v", item.ID, deadErr)
			}
			continue
		}
		select {
		case w.queue <- job{eventID: item.ID, event: event, attempt: item.Attempts}:
			w.observeQueue()
		default:
			w.observeQueue()
			w.observeJob("replay_queue_full")
			return
		}
	}
}

func (w *Worker) renewLease(ctx context.Context, eventID string, done chan<- struct{}) {
	defer close(done)
	interval := w.InboxLease / 3
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.Inbox.Renew(ctx, eventID, w.InboxLease); err != nil {
				if !errors.Is(err, context.Canceled) && !errors.Is(err, eventinbox.ErrLeaseLost) {
					log.Printf("renew Slack inbox event %s: %v", eventID, err)
				}
				if errors.Is(err, eventinbox.ErrLeaseLost) {
					w.observeJob("lease_lost")
				} else if !errors.Is(err, context.Canceled) {
					w.observeJob("renew_error")
				}
				return
			}
		}
	}
}

func (w *Worker) maxAttempts() int {
	if w.MaxAttempts <= 0 {
		return 5
	}
	return w.MaxAttempts
}

func (w *Worker) retryDelay(eventID string, attempt int) time.Duration {
	base := w.RetryBase
	if base <= 0 {
		base = time.Second
	}
	maximum := w.RetryMax
	if maximum <= 0 {
		maximum = time.Minute
	}
	if attempt < 1 {
		attempt = 1
	}
	hash := uint64(1469598103934665603)
	for i := 0; i < len(eventID); i++ {
		hash ^= uint64(eventID[i])
		hash *= 1099511628211
	}
	jitter := 0.8 + float64(hash%401)/1000
	exponent := math.Pow(2, float64(attempt-1))
	delay := time.Duration(math.Min(float64(maximum), float64(base)*exponent*jitter))
	return delay
}

func (w *Worker) goRun(ctx context.Context, fn func(context.Context)) {
	if w.StartGoroutine != nil {
		w.StartGoroutine(fn)
		return
	}
	go fn(ctx)
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

func (w *Worker) observeQueue() {
	if w == nil || w.Observer == nil || w.queue == nil {
		return
	}
	w.Observer.EventInboxQueue(len(w.queue), cap(w.queue))
}

func (w *Worker) observeJob(result string) {
	if w == nil || w.Observer == nil {
		return
	}
	w.Observer.EventInboxJob(result)
	w.observeQueue()
}

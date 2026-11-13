package slackevents

import (
	"context"
	"log"
	"time"
)

func (w *Worker) signalPoll() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *Worker) pollDue(ctx context.Context) {
	for {
		due, err := w.Inbox.NextAvailable(ctx)
		delay := FallbackPollPeriod
		if err != nil {
			log.Printf("schedule Slack inbox: %v", err)
		} else if !due.IsZero() {
			delay = min(delay, max(time.Duration(0), time.Until(due)))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-w.wake:
			timer.Stop()
		case <-timer.C:
		}
		if w.draining() {
			return
		}
		if err := w.Inbox.RecoverExpired(ctx, w.maxAttempts()); err != nil {
			log.Printf("recover expired Slack inbox events: %v", err)
		}
		w.replay(ctx)
	}
}

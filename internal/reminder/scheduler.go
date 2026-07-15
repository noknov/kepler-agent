package reminder

import (
	"context"
	"log"
	"time"
)

type Messenger interface {
	PostMessage(context.Context, string, string, string) (string, error)
}

type Scheduler struct {
	Store     Store
	Messenger Messenger
	Interval  time.Duration
}

func (s Scheduler) Start(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.deliver(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s Scheduler) deliver(ctx context.Context) {
	if s.Store == nil || s.Messenger == nil {
		return
	}
	due, err := s.Store.Due(ctx, time.Now().UTC())
	if err != nil {
		log.Printf("reminder: load due reminders: %v", err)
		return
	}
	for _, r := range due {
		// A reminder can be created from a public channel. Always send it as a
		// direct message so neither its content nor a mention leaks to members
		// of that channel.
		if _, err := s.Messenger.PostMessage(ctx, r.UserID, "", "⏰ 提醒："+r.Message); err != nil {
			log.Printf("reminder: deliver %s: %v", r.ID, err)
			continue
		}
		if err := s.Store.MarkSent(ctx, r.ID, time.Now()); err != nil {
			log.Printf("reminder: mark %s sent: %v", r.ID, err)
		}
	}
}

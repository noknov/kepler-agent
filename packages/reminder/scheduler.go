package reminder

import (
	"context"
	"log"
	"time"

	"github.com/noknov/kepler-agent/packages/infra/redisclient"
)

const (
	defaultPollInterval   = 60 * time.Second
	reminderPubSubChannel = "reminders:new"
)

type Messenger interface {
	PostMessage(context.Context, string, string, string) (string, error)
}

type IdempotentMessenger interface {
	PostMessageWithID(context.Context, string, string, string, string) (string, error)
}

type Scheduler struct {
	Store     Store
	Messenger Messenger
	Interval  time.Duration
	Redis     *redisclient.Client
}

func (s Scheduler) Start(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var wake <-chan struct{}
	if s.Redis != nil {
		wakeC := make(chan struct{}, 1)
		go func() {
			sub := s.Redis.Subscribe(ctx, reminderPubSubChannel)
			defer sub.Close()
			ch := sub.Channel()
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-ch:
					if !ok {
						return
					}
					select {
					case wakeC <- struct{}{}:
					default:
					}
				}
			}
		}()
		wake = wakeC
	}

	for {
		s.deliver(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake:
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
		if err := s.Store.RenewClaim(ctx, r.ID, 5*time.Minute); err != nil {
			log.Printf("reminder: claim lost %s: %v", r.ID, err)
			continue
		}
		// A reminder can be created from a public channel. Always send it as a
		// direct message so neither its content nor a mention leaks to members
		// of that channel.
		var sendErr error
		if messenger, ok := s.Messenger.(IdempotentMessenger); ok {
			_, sendErr = messenger.PostMessageWithID(ctx, r.UserID, "", "⏰ 提醒："+r.Message, "reminder:"+r.ID)
		} else {
			_, sendErr = s.Messenger.PostMessage(ctx, r.UserID, "", "⏰ 提醒："+r.Message)
		}
		if sendErr != nil {
			log.Printf("reminder: deliver %s: %v", r.ID, sendErr)
			continue
		}
		if err := s.Store.MarkSent(ctx, r.ID, time.Now()); err != nil {
			log.Printf("reminder: mark %s sent: %v", r.ID, err)
		}
	}
}

package reminder

import (
	"context"
	"testing"
	"time"
)

type fakeMessenger struct{ channel, thread, text string }

func (m *fakeMessenger) PostMessage(_ context.Context, channel, thread, text string) (string, error) {
	m.channel, m.thread, m.text = channel, thread, text
	return "1", nil
}

func TestSchedulerDeliversReminderAsDirectMessage(t *testing.T) {
	store := &schedulerTestStore{reminders: []Reminder{{ID: "r-dm", UserID: "U-owner", Channel: "C-public", ThreadTS: "123.456", Message: "private task", RunAt: time.Now().Add(-time.Second)}}}
	messenger := &fakeMessenger{}
	Scheduler{Store: store, Messenger: messenger}.deliver(context.Background())
	if messenger.channel != "U-owner" || messenger.thread != "" {
		t.Fatalf("delivery target = channel %q, thread %q; want owner DM", messenger.channel, messenger.thread)
	}
	if messenger.text != "⏰ 提醒：private task" {
		t.Fatalf("delivery text = %q", messenger.text)
	}
}

type schedulerTestStore struct{ reminders []Reminder }

func (s *schedulerTestStore) Create(_ context.Context, reminder Reminder) (Reminder, error) {
	s.reminders = append(s.reminders, reminder)
	return reminder, nil
}
func (s *schedulerTestStore) List(context.Context, string) ([]Reminder, error) {
	return s.reminders, nil
}
func (s *schedulerTestStore) Due(_ context.Context, now time.Time) ([]Reminder, error) {
	var due []Reminder
	for _, reminder := range s.reminders {
		if reminder.SentAt.IsZero() && !reminder.RunAt.After(now) {
			due = append(due, reminder)
		}
	}
	return due, nil
}
func (s *schedulerTestStore) MarkSent(_ context.Context, id string, at time.Time) error {
	for i := range s.reminders {
		if s.reminders[i].ID == id {
			s.reminders[i].SentAt = at
		}
	}
	return nil
}
func (s *schedulerTestStore) Cancel(context.Context, string, string) error { return nil }

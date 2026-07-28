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
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(context.Background(), Reminder{ID: "r-dm", UserID: "U-owner", Channel: "C-public", ThreadTS: "123.456", Message: "private task", RunAt: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{}
	Scheduler{Store: store, Messenger: messenger}.deliver(context.Background())
	if messenger.channel != "U-owner" || messenger.thread != "" {
		t.Fatalf("delivery target = channel %q, thread %q; want owner DM", messenger.channel, messenger.thread)
	}
	if messenger.text != "⏰ 提醒：private task" {
		t.Fatalf("delivery text = %q", messenger.text)
	}
}

package reminder

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
	reminderStore "github.com/noknov/kepler-agent/packages/reminder"
)

func TestCreateListAndCancelReminder(t *testing.T) {
	store := newReminderTestStore()
	scope := agenttool.Scope{UserID: "U1", Values: map[string]string{"channel": "C1", "thread_ts": "123.456"}}
	create := CreateTool{Store: store}
	raw, _ := json.Marshal(map[string]string{"run_at": time.Now().Add(time.Hour).Format(time.RFC3339), "message": "喝水"})
	result, err := create.Execute(context.Background(), agenttool.Call{Arguments: raw, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "提醒已创建") {
		t.Fatalf("unexpected create result: %q", result.Text())
	}
	all, err := store.List(context.Background(), "U1")
	if err != nil || len(all) != 1 {
		t.Fatalf("list = %#v, %v", all, err)
	}
	_, err = (CancelTool{Store: store}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"id":"` + all[0].ID + `"}`), Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	all, err = store.List(context.Background(), "U1")
	if err != nil || len(all) != 0 {
		t.Fatalf("after cancel list = %#v, %v", all, err)
	}
}

func TestCancelCannotCrossUserBoundary(t *testing.T) {
	store := newReminderTestStore()
	created, err := store.Create(context.Background(), reminderStore.Reminder{ID: "r-owner", UserID: "U1", Channel: "C1", Message: "private", RunAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (CancelTool{Store: store}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"id":"` + created.ID + `"}`), Scope: agenttool.Scope{UserID: "U2"}})
	if err == nil {
		t.Fatal("another user must not be able to cancel this reminder")
	}
	all, err := store.List(context.Background(), "U1")
	if err != nil || len(all) != 1 {
		t.Fatalf("owner reminder was changed: %#v, %v", all, err)
	}
}

func TestCreateRejectsPastTime(t *testing.T) {
	store := newReminderTestStore()
	raw := json.RawMessage(`{"run_at":"2000-01-01T00:00:00Z","message":"old"}`)
	_, err := (CreateTool{Store: store}).Execute(context.Background(), agenttool.Call{Arguments: raw, Scope: agenttool.Scope{UserID: "U1", Values: map[string]string{"channel": "C1"}}})
	if err == nil {
		t.Fatal("expected past reminder to be rejected")
	}
}

type reminderTestStore struct {
	mu        sync.Mutex
	reminders map[string]reminderStore.Reminder
}

func newReminderTestStore() *reminderTestStore {
	return &reminderTestStore{reminders: map[string]reminderStore.Reminder{}}
}

func (s *reminderTestStore) Create(_ context.Context, reminder reminderStore.Reminder) (reminderStore.Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.reminders[reminder.ID]; exists {
		return reminderStore.Reminder{}, fmt.Errorf("reminder id already exists")
	}
	reminder.CreatedAt = time.Now().UTC()
	s.reminders[reminder.ID] = reminder
	return reminder, nil
}

func (s *reminderTestStore) List(_ context.Context, userID string) ([]reminderStore.Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var reminders []reminderStore.Reminder
	for _, reminder := range s.reminders {
		if reminder.UserID == userID && reminder.SentAt.IsZero() {
			reminders = append(reminders, reminder)
		}
	}
	return reminders, nil
}

func (s *reminderTestStore) Due(context.Context, time.Time) ([]reminderStore.Reminder, error) {
	return nil, nil
}
func (s *reminderTestStore) MarkSent(context.Context, string, time.Time) error { return nil }
func (s *reminderTestStore) Cancel(_ context.Context, id, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reminder, ok := s.reminders[id]
	if !ok || reminder.UserID != userID || !reminder.SentAt.IsZero() {
		return fmt.Errorf("reminder not found")
	}
	reminder.SentAt = time.Now().UTC()
	s.reminders[id] = reminder
	return nil
}

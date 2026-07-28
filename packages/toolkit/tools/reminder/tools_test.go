package reminder

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	reminderStore "github.com/noknov/slack-copilot-agent/packages/reminder"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

func TestCreateListAndCancelReminder(t *testing.T) {
	store, err := reminderStore.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rt := registry.Runtime{UserID: "U1", Channel: "C1", ThreadTS: "123.456"}
	create := CreateTool{Store: store}
	raw, _ := json.Marshal(map[string]string{"run_at": time.Now().Add(time.Hour).Format(time.RFC3339), "message": "喝水"})
	result, err := create.Execute(context.Background(), raw, rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "提醒已创建") {
		t.Fatalf("unexpected create result: %q", result.Content)
	}
	all, err := store.List(context.Background(), "U1")
	if err != nil || len(all) != 1 {
		t.Fatalf("list = %#v, %v", all, err)
	}
	_, err = (CancelTool{Store: store}).Execute(context.Background(), json.RawMessage(`{"id":"`+all[0].ID+`"}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	all, err = store.List(context.Background(), "U1")
	if err != nil || len(all) != 0 {
		t.Fatalf("after cancel list = %#v, %v", all, err)
	}
}

func TestCancelCannotCrossUserBoundary(t *testing.T) {
	store, err := reminderStore.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(context.Background(), reminderStore.Reminder{ID: "r-owner", UserID: "U1", Channel: "C1", Message: "private", RunAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (CancelTool{Store: store}).Execute(context.Background(), json.RawMessage(`{"id":"`+created.ID+`"}`), registry.Runtime{UserID: "U2"})
	if err == nil {
		t.Fatal("another user must not be able to cancel this reminder")
	}
	all, err := store.List(context.Background(), "U1")
	if err != nil || len(all) != 1 {
		t.Fatalf("owner reminder was changed: %#v, %v", all, err)
	}
}

func TestCreateRejectsPastTime(t *testing.T) {
	store, err := reminderStore.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"run_at":"2000-01-01T00:00:00Z","message":"old"}`)
	_, err = (CreateTool{Store: store}).Execute(context.Background(), raw, registry.Runtime{UserID: "U1", Channel: "C1"})
	if err == nil {
		t.Fatal("expected past reminder to be rejected")
	}
}

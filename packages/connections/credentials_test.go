package connections

import (
	"context"
	"testing"
)

func TestClearProviderDeletesConnection(t *testing.T) {
	path := t.TempDir() + "/connections.json"
	store, err := NewFileStore(path, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	userID := "U1"
	if err := store.UpsertToken(context.Background(), userID, ProviderClickStack, "token", nil, ""); err != nil {
		t.Fatal(err)
	}
	notified := false
	service := Service{
		Store: store,
		OnConnectionChanged: func(ctx context.Context, uid, provider string) error {
			if uid != userID || provider != ProviderClickStack {
				t.Fatalf("notify uid=%q provider=%q", uid, provider)
			}
			notified = true
			return nil
		},
	}
	if err := service.ClearProvider(context.Background(), userID, ProviderClickStack); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), userID, ProviderClickStack); err == nil {
		t.Fatal("expected connection deleted")
	}
	if !notified {
		t.Fatal("expected connection changed notification")
	}
}

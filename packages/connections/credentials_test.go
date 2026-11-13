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

type completionCounter struct{ calls int }

func (c *completionCounter) PublishCompleted(context.Context, string, string, string) error {
	c.calls++
	return nil
}

func TestCredentialInvalidationDoesNotAnnounceOAuthCompletion(t *testing.T) {
	store, err := NewFileStore(t.TempDir()+"/connections.json", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	notifier := &completionCounter{}
	service := Service{Store: store, OAuthCompleted: notifier}
	if err := service.ClearProviderInstance(t.Context(), "U1", ProviderClickStack, "i-eu"); err != nil {
		t.Fatal(err)
	}
	if notifier.calls != 0 {
		t.Fatal("credential invalidation announced successful authorization")
	}
	service.notifyOAuthCompleted(t.Context(), "U1", ProviderClickStack, "i-eu")
	if notifier.calls != 1 {
		t.Fatal("successful OAuth completion was not announced")
	}
}

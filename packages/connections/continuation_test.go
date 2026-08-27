package connections

import (
	"context"
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
)

func TestParseOAuthCompletedPayload(t *testing.T) {
	userID, provider, ok := ParseOAuthCompletedPayload("U123|notion")
	if !ok || userID != "U123" || provider != "notion" {
		t.Fatalf("ParseOAuthCompletedPayload() = (%q, %q, %v)", userID, provider, ok)
	}
	if _, _, ok := ParseOAuthCompletedPayload("bad"); ok {
		t.Fatal("expected invalid payload")
	}
}

func TestRedisContinuationStoreRoundTrip(t *testing.T) {
	client, err := redisclient.New("redis://127.0.0.1:6379/15")
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	defer client.Close()

	store := NewRedisContinuationStore(client)
	ctx := context.Background()
	cont := Continuation{
		UserID:    "U-test",
		Provider:  ProviderNotion,
		SessionID: "C1:T1",
		Channel:   "C1",
		ThreadTS:  "T1",
	}
	if err := store.Save(ctx, cont); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Claim(ctx, cont.UserID, cont.Provider)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("Claim() = (%+v, %v)", loaded, err)
	}
	if loaded[0].Channel != cont.Channel || loaded[0].ThreadTS != cont.ThreadTS {
		t.Fatalf("Claim() = %+v, want %+v", loaded[0], cont)
	}
	if err := store.Release(ctx, cont); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	loaded, err = store.Claim(ctx, cont.UserID, cont.Provider)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("Claim() after release = (%+v, %v)", loaded, err)
	}
	if err := store.Clear(ctx, cont); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	loaded, err = store.Claim(ctx, cont.UserID, cont.Provider)
	if err != nil || len(loaded) != 0 {
		t.Fatalf("Claim() after clear = (%+v, %v)", loaded, err)
	}
}

func TestRedisContinuationStorePublishCompleted(t *testing.T) {
	client, err := redisclient.New("redis://127.0.0.1:6379/15")
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	defer client.Close()

	store := NewRedisContinuationStore(client)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub := client.Subscribe(ctx, OAuthCompletedChannel)
	defer func() { _ = sub.Close() }()
	time.Sleep(100 * time.Millisecond)

	if err := store.PublishCompleted(ctx, "U1", ProviderSlack); err != nil {
		t.Fatalf("PublishCompleted() error = %v", err)
	}
	select {
	case msg := <-sub.Channel():
		userID, provider, ok := ParseOAuthCompletedPayload(msg.Payload)
		if !ok || userID != "U1" || provider != ProviderSlack {
			t.Fatalf("payload = %q", msg.Payload)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for oauth completed event")
	}
}

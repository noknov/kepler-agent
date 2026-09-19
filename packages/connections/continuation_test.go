package connections

import (
	"context"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/infra/redisclient"
)

func TestParseOAuthCompletedPayload(t *testing.T) {
	userID, provider, instanceID, ok := ParseOAuthCompletedPayload("U123|notion")
	if !ok || userID != "U123" || provider != "notion" || instanceID != "" {
		t.Fatalf("ParseOAuthCompletedPayload() = (%q, %q, %q, %v)", userID, provider, instanceID, ok)
	}
	userID, provider, instanceID, ok = ParseOAuthCompletedPayload("U123|notion|i-prod")
	if !ok || userID != "U123" || provider != "notion" || instanceID != "i-prod" {
		t.Fatalf("ParseOAuthCompletedPayload() with instance = (%q, %q, %q, %v)", userID, provider, instanceID, ok)
	}
	if _, _, _, ok := ParseOAuthCompletedPayload("bad"); ok {
		t.Fatal("expected invalid payload")
	}
}

func TestOAuthCompletionNotifiesSurfaceRefresh(t *testing.T) {
	var gotUser, gotProvider string
	service := Service{OnConnectionChanged: func(_ context.Context, userID, provider string) error {
		gotUser, gotProvider = userID, provider
		return nil
	}}
	service.notifyOAuthCompleted(context.Background(), "U1", ProviderClickStack, "i-prod")
	if gotUser != "U1" || gotProvider != ProviderClickStack {
		t.Fatalf("connection refresh notification = (%q, %q)", gotUser, gotProvider)
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
		UserID:     "U-test",
		Provider:   ProviderNotion,
		InstanceID: "i-prod",
		SessionID:  "C1:T1",
		Channel:    "C1",
		ThreadTS:   "T1",
	}
	if err := store.Save(ctx, cont); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Claim(ctx, cont.UserID, cont.Provider, cont.InstanceID)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("Claim() = (%+v, %v)", loaded, err)
	}
	if loaded[0].Channel != cont.Channel || loaded[0].ThreadTS != cont.ThreadTS {
		t.Fatalf("Claim() = %+v, want %+v", loaded[0], cont)
	}
	if err := store.Release(ctx, cont); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	loaded, err = store.Claim(ctx, cont.UserID, cont.Provider, cont.InstanceID)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("Claim() after release = (%+v, %v)", loaded, err)
	}
	if err := store.Clear(ctx, cont); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	loaded, err = store.Claim(ctx, cont.UserID, cont.Provider, cont.InstanceID)
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

	if err := store.PublishCompleted(ctx, "U1", ProviderSlack, "i-prod"); err != nil {
		t.Fatalf("PublishCompleted() error = %v", err)
	}
	select {
	case msg := <-sub.Channel():
		userID, provider, instanceID, ok := ParseOAuthCompletedPayload(msg.Payload)
		if !ok || userID != "U1" || provider != ProviderSlack || instanceID != "i-prod" {
			t.Fatalf("payload = %q", msg.Payload)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for oauth completed event")
	}
}

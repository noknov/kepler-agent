package connections

import (
	"context"
	"os"
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

func continuationRedis(t *testing.T) *redisclient.Client {
	t.Helper()
	address := os.Getenv("REDIS_TEST_URL")
	if address == "" {
		t.Skip("REDIS_TEST_URL must point to an isolated Redis fixture")
	}
	client, err := redisclient.New(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestRedisContinuationStorePublishCompleted(t *testing.T) {
	client := continuationRedis(t)

	store := RedisOAuthCompletionNotifier{Redis: client}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub := client.Subscribe(ctx, OAuthCompletedChannel)
	defer func() { _ = sub.Close() }()
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatal(err)
	}

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

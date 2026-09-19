package connections

import (
	"context"
	"testing"
)

func TestGCPAccessTokenInstancesKeepAccountsSeparate(t *testing.T) {
	store, err := NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.UpsertTokenInstance(ctx, "U1", ProviderGCP, "i-prod", "Production", "prod-token", nil, "prod@example.com", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTokenInstance(ctx, "U1", ProviderGCP, "i-eu", "Europe", "eu-token", nil, "eu@example.com", nil); err != nil {
		t.Fatal(err)
	}
	service := Service{Store: store}
	for _, want := range []struct{ instance, token string }{{"i-prod", "prod-token"}, {"i-eu", "eu-token"}} {
		got, err := service.GCPAccessTokenInstance(ctx, "U1", want.instance)
		if err != nil || got != want.token {
			t.Fatalf("GCPAccessTokenInstance(%q) = (%q, %v), want %q", want.instance, got, err, want.token)
		}
	}
}

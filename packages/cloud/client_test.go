package cloud

import (
	"context"
	"encoding/json"
	"testing"
)

func TestResolveBootstrapFromEnv(t *testing.T) {
	t.Setenv("KEPLER_BOOTSTRAP", `{"provider":"openai","protocol":"kepler","model":"gpt-test"}`)
	info, err := ResolveBootstrap(context.Background(), "http://example.test", "token")
	if err != nil {
		t.Fatal(err)
	}
	if info.Model != "gpt-test" || info.Protocol != "kepler" {
		t.Fatalf("unexpected bootstrap: %+v", info)
	}
}

func TestResolveBootstrapInvalidEnv(t *testing.T) {
	t.Setenv("KEPLER_BOOTSTRAP", `{"model":"only-model"}`)
	_, err := ResolveBootstrap(context.Background(), "http://example.test", "token")
	if err == nil {
		t.Fatal("expected error for incomplete bootstrap env")
	}
}

func TestResolveBootstrapEnvRoundTrip(t *testing.T) {
	want := Bootstrap{Provider: "openai", Protocol: "kepler", Model: "m", Thinking: "high"}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEPLER_BOOTSTRAP", string(raw))
	got, err := ResolveBootstrap(context.Background(), "http://example.test", "token")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

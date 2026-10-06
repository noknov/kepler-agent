package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestBootstrapUsesHTTP1EvenWhenGatewayAdvertisesHTTP2(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			t.Errorf("bootstrap negotiated %s", r.Proto)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing CLI authorization")
		}
		_, _ = w.Write([]byte(`{"model":"test-model","protocol":"kepler"}`))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = original }()
	info, err := FetchBootstrap(context.Background(), server.URL, "test-token")
	if err != nil || info.Model != "test-model" {
		t.Fatalf("bootstrap = %#v, %v", info, err)
	}
}

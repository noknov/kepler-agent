package web

import (
	"context"
	"testing"
	"time"
)

func TestHubMessengerStopStreamDoesNotEndSSE(t *testing.T) {
	hub := NewHub()
	ch := hub.register("U1:conv-1")
	defer hub.deregister("U1:conv-1")

	messenger := NewHubMessenger(hub)
	if err := messenger.StopStream(context.Background(), "web:U1", "conv-1"); err != nil {
		t.Fatalf("StopStream() error = %v", err)
	}

	select {
	case event := <-ch:
		t.Fatalf("StopStream sent unexpected event: %#v", event)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestHubMessengerAppendStreamRoutesChunks(t *testing.T) {
	hub := NewHub()
	ch := hub.register("U1:conv-1")
	defer hub.deregister("U1:conv-1")

	messenger := NewHubMessenger(hub)
	ts, err := messenger.StartStream(context.Background(), "web:U1", "conv-1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	chunks := []map[string]any{{"type": "markdown_text", "text": "hello"}}
	if err := messenger.AppendStream(context.Background(), "web:U1", ts, chunks); err != nil {
		t.Fatalf("AppendStream() error = %v", err)
	}

	select {
	case event := <-ch:
		if event.Kind != kindChunks {
			t.Fatalf("event.Kind = %q, want %q", event.Kind, kindChunks)
		}
		if got := event.Chunks[0]["text"]; got != "hello" {
			t.Fatalf("chunk text = %v, want hello", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for chunk event")
	}
}

func TestHubMessengerScopesRoutesByWebUser(t *testing.T) {
	hub := NewHub()
	u1 := hub.register("U1:conv-1")
	defer hub.deregister("U1:conv-1")
	u2 := hub.register("U2:conv-1")
	defer hub.deregister("U2:conv-1")

	messenger := NewHubMessenger(hub)
	ts, err := messenger.StartStream(context.Background(), "web:U1", "conv-1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	if ts != "U1:conv-1" {
		t.Fatalf("StartStream ts = %q, want U1:conv-1", ts)
	}
	if err := messenger.AppendStream(context.Background(), "web:U1", ts, []map[string]any{{"type": "markdown_text", "text": "u1"}}); err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-u1:
		if got := event.Chunks[0]["text"]; got != "u1" {
			t.Fatalf("u1 chunk text = %v, want u1", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for U1 chunk")
	}
	select {
	case event := <-u2:
		t.Fatalf("U2 received unexpected event: %#v", event)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestWebHubKeyFallsBackForNonWebChannel(t *testing.T) {
	if got := webHubKey("C1", "conv-1"); got != "conv-1" {
		t.Fatalf("webHubKey() = %q, want conv-1", got)
	}
}

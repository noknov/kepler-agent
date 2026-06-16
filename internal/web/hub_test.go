package web

import (
	"context"
	"testing"
	"time"
)

func TestHubMessengerStopStreamDoesNotEndSSE(t *testing.T) {
	hub := NewHub()
	ch := hub.register("conv-1")
	defer hub.deregister("conv-1")

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
	ch := hub.register("conv-1")
	defer hub.deregister("conv-1")

	messenger := NewHubMessenger(hub)
	chunks := []map[string]any{{"type": "markdown_text", "text": "hello"}}
	if err := messenger.AppendStream(context.Background(), "web:U1", "conv-1", chunks); err != nil {
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

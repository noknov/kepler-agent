package slack

import (
	"context"
	"encoding/json"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCodeChannelCreateInheritsPrivacyAndKeepsIdempotency(t *testing.T) {
	var payload map[string]any
	client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/agents.conversations.create" {
			t.Fatalf("path: %s", req.URL.Path)
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"channel_id":"CSPACE"}`)), Request: req}, nil
	}))
	request := CodeChannelCreate{TeamID: "T1", SessionID: "stable", OriginChannel: "CPRIVATE", OriginMessageTS: "1.2"}
	for range 2 {
		if id, err := client.CreateCodeChannel(t.Context(), request); err != nil || id != "CSPACE" {
			t.Fatalf("%s %v", id, err)
		}
	}
	if _, ok := payload["is_private"]; ok {
		t.Fatal("creation overrides source privacy")
	}
	if payload["session_id"] != "stable" || payload["origin_channel_id"] != "CPRIVATE" || payload["origin_message_ts"] != "1.2" {
		t.Fatalf("lost origin/idempotency: %+v", payload)
	}
}

func TestNativeStatusAndStreamOmitThread(t *testing.T) {
	client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := payload["thread_ts"]; ok {
			t.Fatalf("native request has thread: %+v", payload)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"ts":"1.2"}`)), Request: req}, nil
	}))
	if err := client.SetAgentSessionStatus(context.Background(), "CSPACE", "", "U1", "processing"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.StartStream(t.Context(), conversation.StreamStart{Channel: "CSPACE", RecipientUserID: "U1"}); err != nil {
		t.Fatal(err)
	}
}

func TestCodeViewsKeepStableIdentityAndRestrictedCanvasAccess(t *testing.T) {
	client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["channel_id"] != "CSPACE" || payload["view_key"] != "report" || payload["access_level"] != "comment" {
			t.Fatalf("payload: %+v", payload)
		}
		if _, ok := payload["csp"]; ok {
			t.Fatal("view widens CSP")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: req}, nil
	}))
	if err := client.SetCodeView(t.Context(), CodeView{Channel: "CSPACE", Type: "canvas", Key: "report", CanvasID: "F1", AccessLevel: "write"}); err != nil {
		t.Fatal(err)
	}
	for _, view := range []CodeView{{Channel: "CSPACE", Type: "html", Content: "html"}, {Channel: "CSPACE", Type: "diff", Content: strings.Repeat("x", 1_000_001)}, {Channel: "CSPACE", Type: "unsupported", Key: "key"}} {
		if err := client.SetCodeView(t.Context(), view); err == nil {
			t.Fatalf("invalid view accepted: %+v", view.Type)
		}
	}
}

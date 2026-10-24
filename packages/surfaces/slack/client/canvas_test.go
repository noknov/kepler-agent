package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCanvasCreationTargetsRequestedSurface(t *testing.T) {
	for _, channel := range []string{"", "C123"} {
		t.Run("channel="+channel, func(t *testing.T) {
			calls := 0
			client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				want := "/api/canvases.create"
				if channel != "" {
					want = "/api/conversations.canvases.create"
				}
				if req.URL.Path != want {
					t.Errorf("path=%q want=%q", req.URL.Path, want)
				}
				var payload struct {
					Channel string        `json:"channel_id"`
					Title   string        `json:"title"`
					Content CanvasContent `json:"document_content"`
				}
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload.Channel != channel || payload.Title != "Investigation" || payload.Content.Markdown != "Evidence" || payload.Content.Type != "markdown" {
					t.Fatalf("payload=%+v", payload)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"canvas_id":"F123"}`)), Request: req}, nil
			}))
			content := &CanvasContent{Type: "markdown", Markdown: "Evidence"}
			var id string
			var err error
			if channel == "" {
				id, err = client.CreateCanvas(context.Background(), "Investigation", content)
			} else {
				id, err = client.CreateChannelCanvas(context.Background(), channel, "Investigation", content)
			}
			if err != nil || id != "F123" || calls != 1 {
				t.Fatalf("id=%s calls=%d err=%v", id, calls, err)
			}
		})
	}
}

func TestCanvasMutationsDoNotReplayAfterServerFailure(t *testing.T) {
	for _, operation := range []string{"create", "channel_create", "insert"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unavailable")), Request: req}, nil
			}))
			var err error
			switch operation {
			case "create":
				_, err = client.CreateCanvas(context.Background(), "Title", nil)
			case "channel_create":
				_, err = client.CreateChannelCanvas(context.Background(), "C", "Title", nil)
			case "insert":
				err = client.EditCanvas(context.Background(), "F", CanvasEditChange{Operation: "insert_at_end", DocumentContent: &CanvasContent{Type: "markdown", Markdown: "Once"}})
			}
			if err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v: mutation may already have succeeded", calls, err)
			}
		})
	}
}

func TestCanvasMutationsRetryRateLimitRejections(t *testing.T) {
	for _, operation := range []string{"create", "channel_create", "insert"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				status, body := 200, `{"ok":true,"canvas_id":"F123"}`
				if calls == 1 {
					status, body = 429, `{"ok":false,"error":"ratelimited"}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			}))
			var err error
			switch operation {
			case "create":
				_, err = client.CreateCanvas(context.Background(), "Title", nil)
			case "channel_create":
				_, err = client.CreateChannelCanvas(context.Background(), "C", "Title", nil)
			case "insert":
				err = client.EditCanvas(context.Background(), "F", CanvasEditChange{Operation: "insert_at_end"})
			}
			if err != nil || calls != 2 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

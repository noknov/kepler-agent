package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPostMarkdownMessageUsesNativeMarkdownBlock(t *testing.T) {
	var payload struct {
		Channel  string `json:"channel"`
		ThreadTS string `json:"thread_ts"`
		Text     string `json:"text"`
		Blocks   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"blocks"`
	}
	client := &Client{token: "xoxb-test", httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/chat.postMessage" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"ts":"123.456"}`)), Request: r}, nil
	})}}

	markdown := "## Result\n\n- **one**\n- [two](https://example.test)"
	ts, err := client.PostMarkdownMessage(context.Background(), "C1", "100.000", markdown)
	if err != nil {
		t.Fatal(err)
	}
	if ts != "123.456" || payload.Channel != "C1" || payload.ThreadTS != "100.000" || payload.Text != markdown {
		t.Fatalf("response=%q payload=%#v", ts, payload)
	}
	if len(payload.Blocks) != 1 || payload.Blocks[0].Type != "markdown" || payload.Blocks[0].Text != markdown {
		t.Fatalf("blocks=%#v, want one native markdown block", payload.Blocks)
	}
}

func TestFormatFilesIncludesImageMetadata(t *testing.T) {
	text := FormatFiles([]File{{
		ID:         "F123",
		Title:      "checkout screenshot",
		Mimetype:   "image/png",
		Permalink:  "https://slack.example/files/F123",
		URLPrivate: "https://files.slack.com/files-pri/F123",
	}})

	for _, want := range []string{"checkout screenshot", "image/png", "https://slack.example/files/F123", "supported image files"} {
		if !strings.Contains(text, want) {
			t.Fatalf("FormatFiles() = %q, want to contain %q", text, want)
		}
	}
	if strings.Contains(text, "files-pri") {
		t.Fatalf("FormatFiles() leaked private file URL: %q", text)
	}
}

func TestFormatFilesEmpty(t *testing.T) {
	if got := FormatFiles(nil); got != "" {
		t.Fatalf("FormatFiles(nil) = %q, want empty", got)
	}
}

func TestMergeFile(t *testing.T) {
	got := mergeFile(
		File{ID: "F123", Title: "event title"},
		File{
			ID:                 "F456",
			Title:              "info title",
			Mimetype:           "image/png",
			URLPrivateDownload: "https://files.slack.com/file.png",
			Size:               42,
		},
	)

	if got.ID != "F123" || got.Title != "event title" {
		t.Fatalf("mergeFile overwrote primary fields: %#v", got)
	}
	if got.Mimetype != "image/png" || got.URLPrivateDownload == "" || got.Size != 42 {
		t.Fatalf("mergeFile did not fill fallback fields: %#v", got)
	}
}

func TestFormatThreadContextSkipsBotReplies(t *testing.T) {
	got := formatThreadContext([]Message{
		{User: "U123", Text: "first question"},
		{User: "B999", Text: "old bot answer"},
		{BotID: "B01", Text: "app reply"},
		{User: "U123", Text: "follow-up"},
	}, "B999")

	if strings.Contains(got, "old bot answer") || strings.Contains(got, "app reply") {
		t.Fatalf("formatThreadContext() leaked bot replies: %q", got)
	}
	for _, want := range []string{"U123: first question", "U123: follow-up"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatThreadContext() = %q, want %q", got, want)
		}
	}
}

func TestDoRetriesRetryableHTTPStatus(t *testing.T) {
	attempts := 0
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("temporary")),
				Request:    r,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    r,
		}, nil
	})}}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://slack.test/api", strings.NewReader(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := client.do(req, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatal("response was not decoded")
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestRepliesLimitZeroReadsAllPages(t *testing.T) {
	attempts := 0
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if got := r.URL.Query().Get("limit"); got != "200" {
			t.Fatalf("limit = %q, want 200", got)
		}
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true,"messages":[{"user":"U1","text":"one"}],"response_metadata":{"next_cursor":"next"}}`)),
				Request:    r,
			}, nil
		}
		if got := r.URL.Query().Get("cursor"); got != "next" {
			t.Fatalf("cursor = %q, want next", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"messages":[{"user":"U2","text":"two"}]}`)),
			Request:    r,
		}, nil
	})}}

	replies, err := client.Replies(context.Background(), "C1", "100.000", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 2 || replies[0].Text != "one" || replies[1].Text != "two" {
		t.Fatalf("Replies() = %#v, want both pages", replies)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

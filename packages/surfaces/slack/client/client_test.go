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

func TestPostMarkdownMessageFallsBackWhenMarkdownBlockIsUnsupported(t *testing.T) {
	attempts := 0
	client := &Client{token: "xoxb-test", httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if attempts == 1 {
			if _, ok := payload["blocks"]; !ok {
				t.Fatal("first request should use the markdown block")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error":"invalid_blocks"}`)), Request: r}, nil
		}
		if _, ok := payload["blocks"]; ok {
			t.Fatal("fallback request should omit blocks")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"ts":"123.456"}`)), Request: r}, nil
	})}}

	ts, err := client.PostMarkdownMessage(context.Background(), "C1", "T1", "answer")
	if err != nil || ts != "123.456" || attempts != 2 {
		t.Fatalf("ts=%q err=%v attempts=%d", ts, err, attempts)
	}
}

func TestStartStreamIncludesRecipientMetadata(t *testing.T) {
	var payload map[string]any
	client := &Client{token: "xoxb-test", teamID: "T123", httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/chat.startStream" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"ts":"123.456"}`)), Request: r}, nil
	})}}

	ts, err := client.StartStream(context.Background(), "C1", "100.000", "U1")
	if err != nil || ts != "123.456" {
		t.Fatalf("ts=%q err=%v", ts, err)
	}
	if payload["channel"] != "C1" || payload["thread_ts"] != "100.000" || payload["recipient_user_id"] != "U1" || payload["recipient_team_id"] != "T123" {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestAppendAndStopStream(t *testing.T) {
	var appendPayload, stopPayload map[string]any
	client := &Client{token: "xoxb-test", httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/chat.appendStream":
			if err := json.NewDecoder(r.Body).Decode(&appendPayload); err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: r}, nil
		case "/api/chat.stopStream":
			if err := json.NewDecoder(r.Body).Decode(&stopPayload); err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: r}, nil
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
			return nil, nil
		}
	})}}

	if err := client.AppendStream(context.Background(), "C1", "123.456", []map[string]any{{"type": "markdown_text", "text": "hello"}}); err != nil {
		t.Fatal(err)
	}
	if appendPayload["channel"] != "C1" || appendPayload["ts"] != "123.456" {
		t.Fatalf("append payload=%#v", appendPayload)
	}
	if err := client.StopStream(context.Background(), "C1", "123.456"); err != nil {
		t.Fatal(err)
	}
	if stopPayload["channel"] != "C1" || stopPayload["ts"] != "123.456" {
		t.Fatalf("stop payload=%#v", stopPayload)
	}
}

func TestPostMarkdownMessageWithIDUsesStableSlackUUID(t *testing.T) {
	var first, second string
	client := &Client{token: "xoxb-test", httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload struct {
			ClientMessageID string `json:"client_msg_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = payload.ClientMessageID
		} else {
			second = payload.ClientMessageID
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"ts":"123.456"}`)), Request: r}, nil
	})}}
	for range 2 {
		if _, err := client.PostMarkdownMessageWithID(context.Background(), "C1", "T1", "answer", "Ev123"); err != nil {
			t.Fatal(err)
		}
	}
	if first == "" || first != second || len(first) != 36 {
		t.Fatalf("client_msg_id first=%q second=%q", first, second)
	}
}

func TestDownloadFileRejectsUntrustedCredentialTarget(t *testing.T) {
	called := false
	client := &Client{token: "secret", httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	})}}
	_, err := client.DownloadFile(context.Background(), File{ID: "F1", URLPrivateDownload: "https://example.test/steal"}, 100)
	if err == nil || !strings.Contains(err.Error(), "untrusted download host") || called {
		t.Fatalf("err=%v called=%v", err, called)
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

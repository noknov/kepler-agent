package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
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

func TestPostMarkdownMessageSplitsLongTextIntoThreadedParts(t *testing.T) {
	var payloads []map[string]any
	client := &Client{token: "xoxb-test", httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, payload)
		ts := "root"
		if len(payloads) > 1 {
			ts = "part-2"
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"ts":"` + ts + `"}`)), Request: r}, nil
	})}}

	message := strings.Repeat("a", MaxMessageTextRunes+100)
	ts, err := client.PostMarkdownMessage(context.Background(), "C1", "", message)
	if err != nil {
		t.Fatal(err)
	}
	if ts != "root" || len(payloads) != 2 {
		t.Fatalf("ts=%q payloads=%d", ts, len(payloads))
	}
	for index, payload := range payloads {
		text, _ := payload["text"].(string)
		if utf8.RuneCountInString(text) > MaxMessageTextRunes {
			t.Fatalf("part %d has %d runes", index, utf8.RuneCountInString(text))
		}
		if index == 1 && payload["thread_ts"] != "root" {
			t.Fatalf("continuation thread_ts=%#v, want root", payload["thread_ts"])
		}
		blocks, _ := payload["blocks"].([]any)
		if len(blocks) != 1 {
			t.Fatalf("part %d blocks=%#v", index, payload["blocks"])
		}
	}
}

func TestSplitSlackMarkdownKeepsFencedCodeValidAcrossParts(t *testing.T) {
	parts := splitSlackMarkdown("```go\n"+strings.Repeat("x", MaxMessageTextRunes)+"\n```", MaxMessageTextRunes)
	if len(parts) < 2 {
		t.Fatalf("parts=%d, want multiple", len(parts))
	}
	for index, part := range parts {
		if utf8.RuneCountInString(part) > MaxMessageTextRunes {
			t.Fatalf("part %d has %d runes", index, utf8.RuneCountInString(part))
		}
		if strings.Count(part, "```")%2 != 0 {
			t.Fatalf("part %d has unbalanced fenced code: %q", index, part)
		}
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

	ts, err := client.StartStream(context.Background(), slackconversation.StreamStart{Channel: "C1", ThreadTS: "100.000", RecipientUserID: "U1"})
	if err != nil || ts != "123.456" {
		t.Fatalf("ts=%q err=%v", ts, err)
	}
	if payload["channel"] != "C1" || payload["thread_ts"] != "100.000" || payload["recipient_user_id"] != "U1" || payload["recipient_team_id"] != "T123" {
		t.Fatalf("payload=%#v", payload)
	}
	if _, ok := payload["task_display_mode"]; ok {
		t.Fatalf("text stream should not send an unused task display mode: %#v", payload)
	}
}

func TestStartStreamOmitsRecipientMetadataForDM(t *testing.T) {
	var payload map[string]any
	client := &Client{token: "xoxb-test", teamID: "T123", httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"ts":"123.456"}`)), Request: r}, nil
	})}}

	if _, err := client.StartStream(context.Background(), slackconversation.StreamStart{Channel: "D1", ThreadTS: "100.000", RecipientUserID: "U1"}); err != nil {
		t.Fatal(err)
	}
	if payload["channel"] != "D1" || payload["thread_ts"] != "100.000" {
		t.Fatalf("payload=%#v", payload)
	}
	if _, ok := payload["recipient_user_id"]; ok {
		t.Fatalf("DM stream must omit recipient metadata: %#v", payload)
	}
	if _, ok := payload["recipient_team_id"]; ok {
		t.Fatalf("DM stream must omit recipient metadata: %#v", payload)
	}
}

func TestStartStreamIncludesPlanTaskChunks(t *testing.T) {
	var payload map[string]any
	client := &Client{token: "xoxb-test", httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"ts":"123.456"}`)), Request: r}, nil
	})}}
	if _, err := client.StartStream(context.Background(), slackconversation.StreamStart{
		Channel: "D1", ThreadTS: "100.000", TaskDisplayMode: "plan",
		Chunks: []map[string]any{{"type": "task_update", "id": "inspect", "title": "Inspect logs", "status": "in_progress"}},
	}); err != nil {
		t.Fatal(err)
	}
	if payload["task_display_mode"] != "plan" {
		t.Fatalf("payload=%#v", payload)
	}
	if chunks, ok := payload["chunks"].([]any); !ok || len(chunks) != 1 {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestSetAgentSessionStatusUsesAgentSessionsAPI(t *testing.T) {
	var payload map[string]any
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/agents.sessions.setStatus" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"status":"processing","agent_status":"processing"}`)), Request: r}, nil
	})}}
	if err := client.SetAgentSessionStatus(context.Background(), "C1", "100.000", "U1", "processing"); err != nil {
		t.Fatal(err)
	}
	if payload["channel_id"] != "C1" || payload["thread_ts"] != "100.000" || payload["initiator_user_id"] != "U1" || payload["status"] != "processing" {
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
	chunks, ok := appendPayload["chunks"].([]any)
	if !ok || len(chunks) != 1 {
		t.Fatalf("chunks=%#v, want one markdown chunk", appendPayload["chunks"])
	}
	chunk, ok := chunks[0].(map[string]any)
	if !ok || chunk["type"] != "markdown_text" || chunk["text"] != "hello" {
		t.Fatalf("chunk=%#v, want Slack markdown_text chunk", chunks[0])
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

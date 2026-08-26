package slackfiles

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

func TestThreadHistoryDownloadsImagesFromReplies(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	slackClient := slack.NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/conversations.replies"):
			body := `{"ok":true,"messages":[{"user":"U1","text":"pets","ts":"100.000","files":[{"id":"F1","name":"image.png","mimetype":"image/png","size":8}]}]}`
			return jsonResponse(body), nil
		case strings.HasSuffix(r.URL.Path, "/api/files.info"):
			body := `{"ok":true,"file":{"id":"F1","mimetype":"image/png","size":8,"url_private_download":"https://files.slack.com/files-pri/T1-F1/download/image.png"}}`
			return jsonResponse(body), nil
		case strings.Contains(r.URL.Host, "files.slack.com"):
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(string(png))),
			}, nil
		default:
			return nil, fmt.Errorf("unexpected request: %s", r.URL.String())
		}
	}))
	slackClient.SetBotUserID("B1")

	history, err := ThreadHistory(context.Background(), slackClient, "C1", "100.000", "200.000", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d", len(history))
	}
	hasImage := false
	for _, block := range history[0].Content {
		if block.Type == model.ContentImage && strings.HasPrefix(block.ImageURL, "data:image/png;base64,") {
			hasImage = true
		}
	}
	if !hasImage {
		t.Fatalf("expected downloaded image in history: %#v", history[0].Content)
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

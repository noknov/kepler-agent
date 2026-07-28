package slacktool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

type fakeUploader struct {
	channel  string
	threadTS string
	filename string
	data     []byte
}

func (f *fakeUploader) UploadFile(_ context.Context, channel, threadTS, filename string, data []byte) (string, error) {
	f.channel = channel
	f.threadTS = threadTS
	f.filename = filename
	f.data = data
	return "https://slack.com/files/test/screenshot.png", nil
}

func TestSendScreenshotTool_PNG(t *testing.T) {
	uploader := &fakeUploader{}
	tool := SendScreenshotTool{Slack: uploader}

	imgBytes := []byte{0x89, 0x50, 0x4E, 0x47} // PNG magic bytes
	b64 := base64.StdEncoding.EncodeToString(imgBytes)
	dataURI := "data:image/png;base64," + b64

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"data_uri":"`+dataURI+`"}`), registry.Runtime{
		Channel:  "C123",
		ThreadTS: "1234567890.000100",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "uploaded") {
		t.Fatalf("unexpected result: %q", result.Content)
	}
	if uploader.channel != "C123" {
		t.Fatalf("channel = %q", uploader.channel)
	}
	if uploader.filename != "screenshot.png" {
		t.Fatalf("filename = %q", uploader.filename)
	}
	if string(uploader.data) != string(imgBytes) {
		t.Fatalf("data mismatch")
	}
}

func TestSendScreenshotTool_CustomFilename(t *testing.T) {
	uploader := &fakeUploader{}
	tool := SendScreenshotTool{Slack: uploader}

	b64 := base64.StdEncoding.EncodeToString([]byte("jpeg"))
	dataURI := "data:image/jpeg;base64," + b64

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"data_uri":"`+dataURI+`","filename":"login-page.jpg"}`), registry.Runtime{
		Channel: "C456",
	})
	if err != nil {
		t.Fatal(err)
	}
	if uploader.filename != "login-page.jpg" {
		t.Fatalf("filename = %q", uploader.filename)
	}
}

func TestSendScreenshotTool_FromCache(t *testing.T) {
	uploader := &fakeUploader{}
	tool := SendScreenshotTool{Slack: uploader}

	// Simulate pw-screenshot having stored the image in cache.
	imgBytes := []byte{0x89, 0x50, 0x4E, 0x47}
	b64 := base64.StdEncoding.EncodeToString(imgBytes)
	cachedDataURI := "data:image/png;base64," + b64

	cache := registry.NewRuntimeCache()
	cache.Set("pw-screenshot-latest", cachedDataURI)

	// Call with no data_uri — should use cache.
	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`), registry.Runtime{
		Channel:  "C789",
		ThreadTS: "111.222",
		Cache:    cache,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "uploaded") {
		t.Fatalf("expected upload confirmation, got %q", result.Content)
	}
	if string(uploader.data) != string(imgBytes) {
		t.Fatal("cache image data mismatch")
	}
}

func TestSendScreenshotTool_EmptyCacheAndNoDataURI(t *testing.T) {
	tool := SendScreenshotTool{Slack: &fakeUploader{}}
	cache := registry.NewRuntimeCache()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`), registry.Runtime{
		Channel: "C000",
		Cache:   cache,
	})
	if err == nil || !strings.Contains(err.Error(), "no screenshot available") {
		t.Fatalf("expected 'no screenshot available' error, got %v", err)
	}
}

func TestSendScreenshotTool_NoChannel(t *testing.T) {
	tool := SendScreenshotTool{Slack: &fakeUploader{}}
	b64 := base64.StdEncoding.EncodeToString([]byte("x"))
	dataURI := "data:image/png;base64," + b64
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"data_uri":"`+dataURI+`"}`), registry.Runtime{})
	if err == nil || !strings.Contains(err.Error(), "channel") {
		t.Fatalf("expected channel error, got %v", err)
	}
}

func TestParseDataURI(t *testing.T) {
	cases := []struct {
		uri      string
		wantMime string
		wantErr  bool
	}{
		{"data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png")), "image/png", false},
		{"data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("jpg")), "image/jpeg", false},
		{"not-a-data-uri", "", true},
		{"data:image/png;base64,!!!invalid!!!", "", true},
		{"data:image/png,no-encoding-tag", "", true},
	}
	for _, tc := range cases {
		_, mime, err := parseDataURI(tc.uri)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseDataURI(%q): expected error", tc.uri)
			}
		} else {
			if err != nil {
				t.Errorf("parseDataURI(%q): %v", tc.uri, err)
			}
			if mime != tc.wantMime {
				t.Errorf("mime = %q, want %q", mime, tc.wantMime)
			}
		}
	}
}

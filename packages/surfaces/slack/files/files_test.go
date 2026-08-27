package slackfiles

import (
	"context"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

type imageDownloader struct{ calls int }

func (d *imageDownloader) DownloadFile(context.Context, slack.File, int64) ([]byte, error) {
	d.calls++
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, nil
}

func TestImagePartsCapsAggregateImageCount(t *testing.T) {
	downloader := &imageDownloader{}
	files := make([]slack.File, MaxImageCount+3)
	for index := range files {
		files[index] = slack.File{ID: string(rune('A' + index)), Mimetype: "image/png"}
	}
	parts := ImageParts(context.Background(), downloader, files)
	if len(parts) != MaxImageCount || downloader.calls != MaxImageCount {
		t.Fatalf("parts=%d downloads=%d, want %d", len(parts), downloader.calls, MaxImageCount)
	}
}

func TestAttachCapsFileMetadata(t *testing.T) {
	files := make([]slack.File, MaxAttachedFiles+2)
	for index := range files {
		files[index] = slack.File{ID: string(rune('A' + index)), Name: "note.txt", Mimetype: "text/plain"}
	}
	text, _ := Attach(context.Background(), nil, "request", files)
	if want := "[2 additional Slack files omitted; attachment limit is 20]"; !strings.Contains(text, want) {
		t.Fatalf("Attach() omitted-note missing: %q", text)
	}
}

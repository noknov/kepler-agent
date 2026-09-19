package slackfiles

import (
	"context"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

type imageDownloader struct{ calls int }

func (d *imageDownloader) DownloadFile(context.Context, slack.File, int64) ([]byte, error) {
	d.calls++
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, nil
}

func TestImagePartsIsBoundedByBytesNotCount(t *testing.T) {
	downloader := &imageDownloader{}
	files := make([]slack.File, 10)
	for index := range files {
		files[index] = slack.File{ID: string(rune('A' + index)), Mimetype: "image/png"}
	}
	parts := ImageParts(context.Background(), downloader, files)
	if len(parts) != len(files) || downloader.calls != len(files) {
		t.Fatalf("parts=%d downloads=%d, want all %d images under the byte budget", len(parts), downloader.calls, len(files))
	}
}

func TestImagePartsStopsWhenByteBudgetExhausted(t *testing.T) {
	downloader := &imageDownloader{}
	parts := ImagePartsWithBudget(context.Background(), downloader, []slack.File{{ID: "A", Mimetype: "image/png"}}, &ImageBudget{remainingBytes: 0})
	if len(parts) != 0 || downloader.calls != 0 {
		t.Fatalf("parts=%d downloads=%d, want no download under an exhausted budget", len(parts), downloader.calls)
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

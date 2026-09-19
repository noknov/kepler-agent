package slackfiles

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"image/png"
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

func TestAttachDoesNotTruncateFileMetadata(t *testing.T) {
	files := make([]slack.File, 25)
	for index := range files {
		files[index] = slack.File{ID: string(rune('A' + index)), Name: "note.txt", Mimetype: "text/plain"}
	}
	text, _ := Attach(context.Background(), nil, "request", files)
	if strings.Contains(text, "additional Slack files omitted") {
		t.Fatalf("Attach() unexpectedly truncated metadata: %q", text)
	}
	if strings.TrimSpace(text) == "" {
		t.Fatal("Attach() returned no file metadata")
	}
}

func TestShrinkImageForModelDownscalesLargeImages(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3000, 1500))
	for index := range src.Pix {
		src.Pix[index] = 120
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, src); err != nil {
		t.Fatal(err)
	}
	data, mime := shrinkImageForModel(encoded.Bytes(), "image/png")
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mime)
	}
	if len(data) >= encoded.Len() {
		t.Fatalf("shrink did not reduce payload: %d -> %d", encoded.Len(), len(data))
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if bounds := decoded.Bounds(); bounds.Dx() != MaxImageEdge || bounds.Dy() != 784 {
		t.Fatalf("decoded = %dx%d, want %dx%d", bounds.Dx(), bounds.Dy(), MaxImageEdge, 784)
	}
}

func TestShrinkImageForModelKeepsSmallOrUndecodableInput(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 100, 100))
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, src); err != nil {
		t.Fatal(err)
	}
	data, mime := shrinkImageForModel(encoded.Bytes(), "image/png")
	if mime != "image/png" || !bytes.Equal(data, encoded.Bytes()) {
		t.Fatal("small image should pass through unchanged")
	}
	raw := []byte("not an image")
	data, mime = shrinkImageForModel(raw, "image/webp")
	if mime != "image/webp" || !bytes.Equal(data, raw) {
		t.Fatal("undecodable image should pass through unchanged")
	}
}

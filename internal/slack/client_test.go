package slack

import (
	"strings"
	"testing"
)

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

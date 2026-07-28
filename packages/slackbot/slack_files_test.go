package slackbot

import (
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/slack"
)

func TestAppendSlackFilesWithPDFMetadata(t *testing.T) {
	text := appendSlackFiles("analyze this invoice", []slack.File{{
		Title:    "Invoice-9JZUPXEU-0001.pdf",
		Mimetype: "application/pdf",
	}})
	if !strings.Contains(text, "Invoice-9JZUPXEU-0001.pdf") {
		t.Fatalf("appendSlackFiles() = %q", text)
	}
	if !strings.Contains(text, "application/pdf") {
		t.Fatalf("appendSlackFiles() missing mimetype: %q", text)
	}
}

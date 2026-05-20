package app

import (
	"strings"
	"testing"

	"github.com/wati/oncall-agent/internal/slack"
)

func TestFileShareIsUserMessageSubtype(t *testing.T) {
	if !isUserMessageSubtype("") {
		t.Fatal("empty subtype should be treated as a user message")
	}
	if !isUserMessageSubtype("file_share") {
		t.Fatal("file_share subtype should be treated as a user message")
	}
	if isUserMessageSubtype("bot_message") {
		t.Fatal("bot_message subtype should not be treated as a user message")
	}
}

func TestAppendSlackFiles(t *testing.T) {
	text := appendSlackFiles("please check this", []slack.File{{
		Title:    "error screenshot",
		Mimetype: "image/jpeg",
	}})

	if !strings.Contains(text, "please check this") || !strings.Contains(text, "error screenshot") {
		t.Fatalf("appendSlackFiles() = %q, want original text and file metadata", text)
	}
}

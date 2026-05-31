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

func TestNormalizedImageMIME(t *testing.T) {
	tests := map[string]slack.File{
		"image/png":  {Mimetype: "image/png"},
		"image/jpeg": {Filetype: "jpg"},
		"image/webp": {Filetype: "webp"},
		"":           {Mimetype: "application/pdf", Filetype: "pdf"},
	}
	for want, file := range tests {
		if got := normalizedImageMIME(file); got != want {
			t.Fatalf("normalizedImageMIME(%#v) = %q, want %q", file, got, want)
		}
	}
}

func TestSniffImageMIME(t *testing.T) {
	tests := map[string][]byte{
		"image/png":  {0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		"image/jpeg": {0xff, 0xd8, 0xff, 0xe0},
		"image/webp": {'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P'},
		"image/gif":  {'G', 'I', 'F', '8', '9', 'a'},
		"":           []byte("<html><body>not an image</body></html>"),
	}
	for want, data := range tests {
		if got := sniffImageMIME(data); got != want {
			t.Fatalf("sniffImageMIME(%q) = %q, want %q", data, got, want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "U123"); got != "U123" {
		t.Fatalf("firstNonEmpty() = %q, want U123", got)
	}
}

func TestIsDMChannel(t *testing.T) {
	if !isDMChannel("D123") {
		t.Fatal("D channel should be treated as DM")
	}
	if isDMChannel("C123") {
		t.Fatal("C channel should not be treated as DM")
	}
}

func TestIsChannelMention(t *testing.T) {
	tests := []struct {
		name string
		ev   slack.Event
		want bool
	}{
		{
			name: "thread app mention",
			ev:   slack.Event{Type: "app_mention", Channel: "C123", TS: "1717000000.000200", ThreadTS: "1717000000.000100"},
			want: true,
		},
		{
			name: "top level app mention",
			ev:   slack.Event{Type: "app_mention", Channel: "C123", TS: "1717000000.000100"},
			want: true,
		},
		{
			name: "plain thread message",
			ev:   slack.Event{Type: "message", Channel: "C123", TS: "1717000000.000200", ThreadTS: "1717000000.000100"},
		},
		{
			name: "dm app mention",
			ev:   slack.Event{Type: "app_mention", Channel: "D123", ChannelType: "im", TS: "1717000000.000200", ThreadTS: "1717000000.000100"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isChannelMention(tt.ev); got != tt.want {
				t.Fatalf("isChannelMention() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGitFetchEnvDisablesTerminalPrompt(t *testing.T) {
	env := gitFetchEnv()
	if !containsEnv(env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatal("git fetch env should disable terminal prompts")
	}
}

func containsEnv(env []string, value string) bool {
	for _, item := range env {
		if item == value {
			return true
		}
	}
	return false
}

package slack

import "testing"

func TestMarkdownToMrkdwnConvertsBareSlackUserIDs(t *testing.T) {
	got := MarkdownToMrkdwn("Author: @U085SRJFCLX")
	want := "Author: <@U085SRJFCLX>"
	if got != want {
		t.Fatalf("MarkdownToMrkdwn() = %q, want %q", got, want)
	}
}

func TestMarkdownToMrkdwnDoesNotDoubleConvertSlackMentions(t *testing.T) {
	got := MarkdownToMrkdwn("Author: <@U085SRJFCLX>")
	want := "Author: <@U085SRJFCLX>"
	if got != want {
		t.Fatalf("MarkdownToMrkdwn() = %q, want %q", got, want)
	}
}

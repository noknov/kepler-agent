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

func TestMarkdownToMrkdwnStripsCodeBlockLanguageHints(t *testing.T) {
	input := "code:\n```go\nfunc main() {}\n```\nend"
	got := MarkdownToMrkdwn(input)
	want := "code:\n```\nfunc main() {}\n```\nend"
	if got != want {
		t.Fatalf("MarkdownToMrkdwn() =\n%s\nwant:\n%s", got, want)
	}
}

func TestMarkdownToMrkdwnKeepsBareCodeBlocks(t *testing.T) {
	input := "```\nplain code\n```"
	got := MarkdownToMrkdwn(input)
	if got != input {
		t.Fatalf("MarkdownToMrkdwn() = %q, want %q", got, input)
	}
}

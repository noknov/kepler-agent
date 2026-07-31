package slackhandler

import (
	"context"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/slack"
	"github.com/noknov/slack-copilot-agent/packages/slackfiles"
)

func appendSlackFiles(text string, files []slack.File) string {
	return slackfiles.Append(text, files)
}

func (h *Handler) attachSlackFiles(ctx context.Context, text string, files []slack.File) (string, []llm.ContentPart) {
	return slackfiles.Attach(ctx, h.Slack, text, files)
}

func ShouldAttemptSlackTextExcerpt(file slack.File) bool {
	return !slack.IsPDFFile(file) && slackfiles.NormalizedImageMIME(file) == ""
}

func formatBytes(n int64) string {
	return slackfiles.FormatBytes(n)
}

func NormalizedImageMIME(file slack.File) string {
	return slackfiles.NormalizedImageMIME(file)
}

func SniffImageMIME(data []byte) string {
	return slackfiles.SniffImageMIME(data)
}

const (
	maxSlackImageBytes   = slackfiles.MaxImageBytes
	maxSlackPDFBytes     = slackfiles.MaxPDFBytes
	maxSlackPDFTextChars = slackfiles.MaxPDFTextChars
	maxSlackTextBytes    = 16 << 20
	maxSlackTextChars    = slack.DefaultMaxTextExtractChars
)

package slackbot

import (
	"context"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/slack"
	"github.com/noknov/slack-copilot-agent/packages/slackfiles"
)

func appendSlackFiles(text string, files []slack.File) string {
	return slackfiles.Append(text, files)
}

func (s *Server) attachSlackFiles(ctx context.Context, text string, files []slack.File) (string, []llm.ContentPart) {
	return slackfiles.Attach(ctx, s.slack, text, files)
}

func shouldAttemptSlackTextExcerpt(file slack.File) bool {
	return !slack.IsPDFFile(file) && slackfiles.NormalizedImageMIME(file) == ""
}

func formatBytes(n int64) string {
	return slackfiles.FormatBytes(n)
}

func normalizedImageMIME(file slack.File) string {
	return slackfiles.NormalizedImageMIME(file)
}

func sniffImageMIME(data []byte) string {
	return slackfiles.SniffImageMIME(data)
}

const (
	maxSlackImageBytes   = slackfiles.MaxImageBytes
	maxSlackPDFBytes     = slackfiles.MaxPDFBytes
	maxSlackPDFTextChars = slackfiles.MaxPDFTextChars
	maxSlackTextBytes    = 16 << 20
	maxSlackTextChars    = slack.DefaultMaxTextExtractChars
)

package slackhandler

import (
	"context"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
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

func canonicalContent(parts []llm.ContentPart) []model.Content {
	out := make([]model.Content, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			out = append(out, model.Content{Type: model.ContentText, Text: part.Text})
		case "image_url":
			if part.ImageURL != nil && part.ImageURL.URL != "" {
				out = append(out, model.Content{Type: model.ContentImage, ImageURL: part.ImageURL.URL})
			}
		}
	}
	return out
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

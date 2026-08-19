package slackfiles

import (
	"context"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

const (
	MaxThreadHistoryMessages  = 50
	MaxThreadHistoryTextBytes = 64 << 10
	MaxThreadHistoryImages    = 8
)

// ThreadHistory loads prior Slack messages with file metadata and supported images.
func ThreadHistory(ctx context.Context, c *slack.Client, channel, threadTS, beforeTS string, limit int) []model.Message {
	if c == nil {
		return nil
	}
	if limit <= 0 || limit > MaxThreadHistoryMessages {
		limit = MaxThreadHistoryMessages
	}
	raw, err := c.ReadConversation(ctx, slack.ReadTarget{
		Channel:  channel,
		ThreadTS: threadTS,
		LatestTS: beforeTS,
	}, limit+1)
	if err != nil {
		return nil
	}
	out := make([]model.Message, 0, min(limit, len(raw)))
	bytesUsed := 0
	budget := ThreadImageBudget()
	for _, msg := range raw {
		historyMsg, ok := HistoryMessage(ctx, c, msg, c.BotUserID(), budget)
		if !ok {
			continue
		}
		textSize := len(historyMsg.Text())
		if bytesUsed+textSize > MaxThreadHistoryTextBytes || len(out) >= limit {
			break
		}
		bytesUsed += textSize
		out = append(out, historyMsg)
	}
	return out
}

// HistoryMessage converts one Slack message into model history with attachments.
func HistoryMessage(ctx context.Context, d Downloader, msg slack.Message, botUserID string, budget *ImageBudget) (model.Message, bool) {
	text := strings.TrimSpace(slack.NormalizeMentions(msg.Text, botUserID))
	text = Append(text, msg.Files)
	if excerpt := PDFExcerpts(ctx, d, msg.Files); excerpt != "" {
		text = strings.TrimSpace(text)
		if text == "" {
			text = excerpt
		} else {
			text += "\n\n" + excerpt
		}
	}
	parts := ImagePartsWithBudget(ctx, d, msg.Files, budget)
	if text == "" && len(parts) == 0 {
		return model.Message{}, false
	}
	role := model.RoleUser
	if msg.User == botUserID {
		role = model.RoleAssistant
	} else if text != "" {
		text = "Slack user " + msg.User + ": " + text
	}
	return model.Message{Role: role, Content: JoinTextAndImages(text, parts)}, true
}

func JoinTextAndImages(text string, parts []llm.ContentPart) []model.Content {
	content := make([]model.Content, 0, 1+len(parts))
	if strings.TrimSpace(text) != "" {
		content = append(content, model.Content{Type: model.ContentText, Text: text})
	}
	content = append(content, ModelContent(parts)...)
	return content
}

func ModelContent(parts []llm.ContentPart) []model.Content {
	out := make([]model.Content, 0, len(parts))
	for _, part := range parts {
		if part.Type == "image_url" && part.ImageURL != nil && part.ImageURL.URL != "" {
			out = append(out, model.Content{Type: model.ContentImage, ImageURL: part.ImageURL.URL})
		}
	}
	return out
}

// CollectImageContent returns unique image blocks from Slack history messages.
func CollectImageContent(messages []model.Message) []model.Content {
	images := make([]model.Content, 0, len(messages))
	seen := make(map[string]struct{})
	for _, message := range messages {
		for _, block := range message.Content {
			if block.Type != model.ContentImage || block.ImageURL == "" {
				continue
			}
			if _, ok := seen[block.ImageURL]; ok {
				continue
			}
			seen[block.ImageURL] = struct{}{}
			images = append(images, block)
		}
	}
	return images
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

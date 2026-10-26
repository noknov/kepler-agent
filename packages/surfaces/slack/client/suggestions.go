package slack

import (
	"context"
	"fmt"
	"strings"
)

type SuggestedPrompt struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

// SetSuggestedPrompts updates the agent_view Messages tab. Including thread_ts
// silently disables this method for agent apps; legacy assistant_view is not
// part of this wire contract. Updates are idempotent and do not send a message.
func (c *Client) SetSuggestedPrompts(ctx context.Context, channel, title string, prompts []SuggestedPrompt) error {
	if strings.TrimSpace(channel) == "" || len(prompts) < 1 || len(prompts) > 4 {
		return fmt.Errorf("suggested prompts require a channel and 1..4 entries")
	}
	for _, prompt := range prompts {
		if strings.TrimSpace(prompt.Title) == "" || strings.TrimSpace(prompt.Message) == "" {
			return fmt.Errorf("suggested prompt title and message are required")
		}
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.postJSON(ctx, "assistant.threads.setSuggestedPrompts", map[string]any{"channel_id": channel, "title": title, "prompts": prompts}, &out); err != nil {
		return err
	}
	if !out.OK {
		return slackAPIError{Method: "assistant.threads.setSuggestedPrompts", Code: out.Error}
	}
	return nil
}

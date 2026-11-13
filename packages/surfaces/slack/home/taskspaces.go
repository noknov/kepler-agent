package slackhome

import (
	"context"
	"log"
	"net/url"
)

func (c Controller) taskSpaceBlocks(ctx context.Context, user string) []map[string]any {
	if c.Spaces == nil || c.TeamID == "" || !c.Access.AllowsUser(user) {
		return nil
	}
	blocks := []map[string]any{dividerBlock(), headerBlock("Task spaces")}
	spaces, err := c.Spaces.ListOwned(ctx, c.TeamID, user)
	if err != nil {
		log.Printf("list personal task spaces failed: %T", err)
		return append(blocks, sectionBlockWithFields("Task spaces are temporarily unavailable."))
	}
	if len(spaces) == 0 {
		return append(blocks, sectionBlockWithFields("No task spaces yet. Ask the agent to create one for your next task."))
	}
	for _, space := range spaces {
		// Slack's documented redirect rechecks the viewer's channel membership.
		// This opens the whole native session; a thread URL is not appropriate.
		link := "https://slack.com/app_redirect?" + url.Values{"team": {c.TeamID}, "channel": {space.Channel}}.Encode()
		blocks = append(blocks, map[string]any{
			"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "<#" + space.Channel + ">"},
			"accessory": map[string]any{"type": "button", "text": map[string]any{"type": "plain_text", "text": "Open"}, "url": link, "action_id": "open_task_space"},
		})
	}
	return blocks
}

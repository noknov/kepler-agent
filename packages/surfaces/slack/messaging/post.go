package messaging

import (
	"context"
	"errors"

	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

var errSlackClientRequired = errors.New("slack client is not configured")

// PostAsConnectedUser sends a message with the standard 斗包 attribution footer.
func PostAsConnectedUser(ctx context.Context, client *slack.Client, channel, threadTS, text string, attr Attribution) (string, error) {
	if client == nil {
		return "", errSlackClientRequired
	}
	blocks := BlocksWithAttribution(text, attr.Text())
	if len(blocks) == 0 {
		return client.PostMessage(ctx, channel, threadTS, text)
	}
	return client.PostMessageBlocks(ctx, channel, threadTS, text, blocks)
}

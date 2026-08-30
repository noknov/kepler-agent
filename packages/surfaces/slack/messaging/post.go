package messaging

import (
	"context"
	"errors"

	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

var errSlackClientRequired = errors.New("slack client is not configured")

// PostAsConnectedUser sends a message with the standard 斗包 attribution footer.
// deliveryID must identify the originating tool call so multi-part delivery is
// safe to retry after a partial Slack API failure.
func PostAsConnectedUser(ctx context.Context, client *slack.Client, channel, threadTS, text, deliveryID string, attr Attribution) (string, error) {
	if client == nil {
		return "", errSlackClientRequired
	}
	attribution := attr.Text()
	if attribution == "" {
		return client.PostChunkedMessage(ctx, channel, threadTS, text, deliveryID, slack.MaxMessageTextRunes, nil)
	}
	return client.PostChunkedMessage(ctx, channel, threadTS, text, deliveryID, slack.MaxSectionTextRunes, func(part string) []map[string]any {
		return BlocksWithAttribution(part, attribution)
	})
}

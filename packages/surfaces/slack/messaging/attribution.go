package messaging

import "strings"

// Attribution configures the footer shown on user-connection Slack posts.
type Attribution struct {
	BotUserID string
	Name      string
	Footer    string
}

func (a Attribution) Text() string {
	if footer := strings.TrimSpace(a.Footer); footer != "" {
		return footer
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = "斗包"
	}
	if botUserID := strings.TrimSpace(a.BotUserID); botUserID != "" {
		return "Sent using <@" + botUserID + "|" + name + ">"
	}
	return "Sent using " + name
}

func BlocksWithAttribution(body, attribution string) []map[string]any {
	body = strings.TrimSpace(body)
	attribution = strings.TrimSpace(attribution)
	if body == "" || attribution == "" {
		return nil
	}
	return []map[string]any{
		{
			"type": "section",
			"text": map[string]any{
				"type": "mrkdwn",
				"text": body,
			},
		},
		{
			"type": "context",
			"elements": []map[string]any{
				{
					"type": "mrkdwn",
					"text": attribution,
				},
			},
		},
	}
}

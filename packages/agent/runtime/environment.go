package runtime

import (
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/environment"
	"github.com/noknov/slack-copilot-agent/packages/agent/model"
)

func insertEnvironmentFragment(messages []model.Message, fragment model.Message) []model.Message {
	if strings.TrimSpace(fragment.Text()) == "" {
		return messages
	}
	insertAt := 0
	for index, message := range messages {
		if message.Role == model.RoleSystem {
			insertAt = index + 1
			continue
		}
		break
	}
	out := make([]model.Message, 0, len(messages)+1)
	out = append(out, messages[:insertAt]...)
	out = append(out, fragment)
	out = append(out, messages[insertAt:]...)
	return out
}

func overlayEnvironment(messages []model.Message, config environment.Config) []model.Message {
	if strings.TrimSpace(config.Message().Text()) == "" {
		return messages
	}
	return insertEnvironmentFragment(messages, config.Message())
}

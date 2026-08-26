package slacktool

import (
	"encoding/json"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

func readTargetInput(call tool.Call) slack.ReadTargetInput {
	var args struct {
		User, Channel, Link string
		ThreadTS            string `json:"thread_ts"`
	}
	_ = json.Unmarshal(call.Arguments, &args)
	scope := call.Scope.Values
	return slack.ReadTargetInput{
		Channel:        strings.TrimSpace(args.Channel),
		User:           strings.TrimSpace(args.User),
		Link:           strings.TrimSpace(args.Link),
		ThreadTS:       strings.TrimSpace(args.ThreadTS),
		ScopeChannel:   strings.TrimSpace(scope["channel"]),
		ScopeThreadTS:  strings.TrimSpace(scope["thread_ts"]),
		ScopeMessageTS: strings.TrimSpace(scope["message_ts"]),
	}
}

package slackhandler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/safety"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

func TestDMViewingContextUsesMessageSnapshot(t *testing.T) {
	conv := &triggerConversation{}
	h := Handler{Conv: conv, Access: safety.NewAccessPolicy([]string{"U1"}, []string{"C_ALLOWED"})}
	var event slack.Event
	if err := json.Unmarshal([]byte(`{"type":"message","team_id":"T1","user":"U1","channel":"D1","channel_type":"im","ts":"2.0","text":"summarize what I am viewing","app_context":{"entities":[{"type":"slack#/types/channel_id","value":"C123","team_id":"T1"}]}}`), &event); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(context.Background(), "first", event); err != nil {
		t.Fatal(err)
	}
	if len(conv.requests) != 1 {
		t.Fatal("missing request")
	}
	request := conv.requests[0]
	message := request.Message()
	if request.Channel != "D1" || message.Text() != "summarize what I am viewing" {
		t.Fatal("viewing reference rewrote text or delivery", request)
	}
	if len(message.Content) != 2 || message.Content[1].Type != model.ContentJSON || !strings.Contains(string(message.Content[1].JSON), `"channel_id":"C123"`) {
		t.Fatalf("lost message-time viewing context: %+v", message)
	}
	// A later ambient UI event cannot mutate a previously accepted snapshot.
	if err := h.Handle(context.Background(), "ambient", slack.Event{Type: "app_context_changed", User: "U1", Context: slack.AppContext{Entities: []slack.ContextEntity{{Type: "slack#/types/channel_id", Value: "C456", TeamID: "T1"}}}}); err != nil {
		t.Fatal(err)
	}

	// Decode into a fresh value, as Events API ingress does for every payload.
	event = slack.Event{Type: "message", TeamID: "T1", User: "U1", Channel: "D1", TS: "3.0", Text: "a new question"}
	if err := h.Handle(context.Background(), "second", event); err != nil {
		t.Fatal(err)
	}
	if len(conv.requests) != 2 || len(conv.requests[1].Message().Content) != 1 {
		t.Fatal("reused stale viewing context")
	}
}

func TestViewingContextIgnoresUnsupportedForeignAndChannelPayloads(t *testing.T) {
	for _, test := range []struct{ name, channel, entityType, entityTeam, value string }{
		{"foreign workspace", "D1", "slack#/types/channel_id", "T2", "C123"},
		{"missing workspace", "D1", "slack#/types/channel_id", "", "C123"},
		{"unknown entity", "D1", "slack#/types/canvas_id", "T1", "F123"},
		{"invalid channel", "D1", "slack#/types/channel_id", "T1", "C123 instruction"},
		{"channel message", "C1", "slack#/types/channel_id", "T1", "C123"},
	} {
		t.Run(test.name, func(t *testing.T) {
			conv := &triggerConversation{}
			h := Handler{Conv: conv}
			h.Cfg.Slack.BotUserID = "BOT"
			raw, _ := json.Marshal(map[string]any{"type": "message", "team_id": "T1", "user": "U1", "channel": test.channel, "ts": "2.0", "text": "<@BOT> question", "app_context": map[string]any{"entities": []any{map[string]string{"type": test.entityType, "value": test.value, "team_id": test.entityTeam}}}})
			var event slack.Event
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if test.channel == "C1" {
				event.Type = "app_mention"
			}
			if err := h.Handle(context.Background(), test.name, event); err != nil {
				t.Fatal(err)
			}
			if len(conv.requests) != 1 || len(conv.requests[0].Message().Content) != 1 {
				t.Fatal("accepted unsupported reference", conv.requests)
			}
		})
	}
}

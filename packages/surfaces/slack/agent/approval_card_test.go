package slackagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/session"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type approvalCardMessenger struct {
	connectionCardMessenger
	destination, thread, text string
}

func (m *approvalCardMessenger) PostMessageBlocks(_ context.Context, channel, thread, text string, blocks []map[string]any) (string, error) {
	m.destination, m.thread, m.text, m.blocks = channel, thread, text, blocks
	return "card", nil
}

func TestApprovalCardNotifiesOwnerAndOnlyRequesterCanConfirm(t *testing.T) {
	for _, name := range []string{"github-approve_pr", "other-write"} {
		t.Run(name, func(t *testing.T) {
			channel := "C-shared"
			messenger := &approvalCardMessenger{}
			stream := newSlackStream(context.Background(), messenger, conversation.Request{Channel: channel, ThreadTS: "original"})
			call := tool.Call{ID: "call", Name: name, Scope: tool.Scope{UserID: "requester"}, Arguments: json.RawMessage(`{"url":"https://github.com/example/repo/pull/1","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)}
			stream.approvalNotifyUserID = "owner"
			stream.requestApproval(transcript.Event{Type: transcript.ApprovalRequested, TurnID: "turn", ToolCall: &call, Metadata: json.RawMessage(`{"effects":["external_write"]}`)})
			if messenger.destination != channel || messenger.thread != "original" {
				t.Fatalf("notification=%+v", messenger)
			}
			raw, _ := json.Marshal(messenger.blocks)
			if strings.Contains(messenger.blocks[0]["text"].(map[string]any)["text"].(string), "<@owner>") != (name == "github-approve_pr") {
				t.Fatalf("unexpected cc: %s", raw)
			}
			if strings.Count(string(raw), `"visible_to_user_ids":["requester"]`) != 2 {
				t.Fatalf("approval buttons are not restricted: %s", raw)
			}
			if !strings.Contains(string(raw), session.ID(channel, "original")) || !strings.Contains(string(raw), "head_sha") {
				t.Fatalf("card lacks immutable origin/head: %s", raw)
			}
		})
	}
}

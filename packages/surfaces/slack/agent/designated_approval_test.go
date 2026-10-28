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

func TestDesignatedApprovalCardNotifiesOperatorAndBindsOriginalSession(t *testing.T) {
	for _, channel := range []string{"D-requester", "C-shared"} {
		t.Run(channel, func(t *testing.T) {
			messenger := &approvalCardMessenger{}
			stream := newSlackStream(context.Background(), messenger, conversation.Request{Channel: channel, ThreadTS: "original"})
			call := tool.Call{ID: "call", Name: "github-approve_pr", Scope: tool.Scope{UserID: "requester"}, Arguments: json.RawMessage(`{"url":"https://github.com/example/repo/pull/1","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)}
			stream.requestApproval(transcript.Event{Type: transcript.ApprovalRequested, TurnID: "turn", ToolCall: &call, Metadata: json.RawMessage(`{"approver_user_id":"owner","effects":["external_write"]}`)})
			if messenger.destination != channel || messenger.thread != "original" || !strings.Contains(messenger.text, "<@owner>") {
				t.Fatalf("notification=%+v", messenger)
			}
			raw, _ := json.Marshal(messenger.blocks)
			if strings.Count(string(raw), `"visible_to_user_ids":["owner"]`) != 2 {
				t.Fatalf("approval buttons are not restricted: %s", raw)
			}
			if !strings.Contains(string(raw), session.ID(channel, "original")) || !strings.Contains(string(raw), "head_sha") {
				t.Fatalf("card lacks immutable origin/head: %s", raw)
			}
		})
	}
}

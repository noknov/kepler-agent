package slacktool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/safety"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/taskspace"
)

type taskViewTransport func(*http.Request) (*http.Response, error)

func (f taskViewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type taskViewGrant struct{}

func (taskViewGrant) Get(context.Context, string, string) (taskspace.Space, error) {
	return taskspace.Space{TeamID: "T1", Channel: "CSPACE", OwnerID: "U1", OriginChannel: "CORIGIN", OriginMessageTS: "1.0"}, nil
}
func (taskViewGrant) Save(context.Context, taskspace.Space) error { return nil }

func TestTaskViewToolsUseAuthenticatedNativeTargetAndRetainEffects(t *testing.T) {
	writes := 0
	client := slack.NewTestClient(taskViewTransport(func(req *http.Request) (*http.Response, error) {
		body := `{"ok":true}`
		if strings.HasSuffix(req.URL.Path, "conversations.info") {
			body = `{"ok":true,"channel":{"id":"CSPACE","properties":{"record_channel":{"record_type":"agent_channel"},"agent_session":{"encoded_agent_bot_user_ids":["BOT"]}}}}`
		} else {
			var payload map[string]any
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["channel"] != "CSPACE" && payload["channel_id"] != "CSPACE" {
				t.Fatalf("unbound target: %+v", payload)
			}
			writes++
			if strings.HasSuffix(req.URL.Path, "getCanvas") {
				body = `{"ok":true,"canvas_id":"F1","content":"Plan","comments":[]}`
			}
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	}))
	spaces := &taskspace.Service{Slack: client, Store: taskViewGrant{}, BotUserID: "BOT", Access: safety.NewAccessPolicy([]string{"U1", "U2"}, []string{"CORIGIN"})}
	scope := tool.Scope{UserID: "U1", Values: map[string]string{"surface": "slack", "team_id": "T1", "channel": "CSPACE", "session_kind": string(conversation.CodeChannelSession)}}
	cases := []struct {
		item  tool.Tool
		args  string
		write bool
	}{
		{ListTaskViewsTool{spaces}, `{"channel":"OTHER"}`, false},
		{ReadTaskCanvasTool{spaces}, `{"canvas_id":"F1","channel":"OTHER"}`, false},
		{UpdateTaskCanvasTool{spaces}, `{"canvas_id":"F1","content":"Revised","channel":"OTHER"}`, true},
		{RemoveTaskViewTool{spaces}, `{"view_id":"V1","channel":"OTHER"}`, true},
	}
	for _, tt := range cases {
		t.Run(tt.item.Descriptor().Name, func(t *testing.T) {
			effects := tt.item.Descriptor().Effects
			external := false
			for _, effect := range effects {
				external = external || effect == tool.EffectExternalWrite
			}
			if external != tt.write {
				t.Fatalf("incorrect write authority: %v", effects)
			}
			if _, err := tt.item.Execute(t.Context(), tool.Call{Scope: scope, Arguments: json.RawMessage(tt.args)}); err != nil {
				t.Fatal(err)
			}
			before := writes
			for _, invalid := range []tool.Scope{{UserID: "U2", Values: scope.Values}, {UserID: "U1", Values: map[string]string{"surface": "slack", "team_id": "T1", "channel": "CSPACE", "thread_ts": "1.0"}}} {
				if _, err := tt.item.Execute(t.Context(), tool.Call{Scope: invalid, Arguments: json.RawMessage(tt.args)}); err == nil {
					t.Fatal("unauthorized native operation accepted")
				}
			}
			if writes != before {
				t.Fatal(fmt.Sprintf("denied operation reached upstream: %d -> %d", before, writes))
			}
		})
	}
}

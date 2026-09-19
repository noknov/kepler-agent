package slackagent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/connections"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type codedSlackStatusError string

func (e codedSlackStatusError) Error() string          { return "sensitive upstream detail" }
func (e codedSlackStatusError) SlackErrorCode() string { return string(e) }

func TestApprovalDescriptionRendersPolicyAndEffects(t *testing.T) {
	metadata := []byte(`{"type":"require_approval","reason":"this action changes data or an external service","rule":"user_confirmation","effects":["external_write","network"]}`)
	got := approvalDescription("slack-user_post_message", []byte(`{"channel":"C1","text":"hi"}`), metadata)
	for _, want := range []string{
		"*Action:* slack-user_post_message",
		"*Effect:* external write, network",
		"*Why:* this action changes data or an external service",
		"*Policy:* user_confirmation",
		`"channel": "C1"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("approvalDescription() missing %q:\n%s", want, got)
		}
	}
}

func TestApprovalDescriptionHandlesMissingMetadata(t *testing.T) {
	got := approvalDescription("", []byte(`{}`), nil)
	if !strings.Contains(got, "*Action:* this action") {
		t.Fatalf("approvalDescription() = %q, want a default action label", got)
	}
	if strings.Contains(got, "*Effect:*") || strings.Contains(got, "*Why:*") {
		t.Fatalf("approvalDescription() = %q, want no invented effect or reason", got)
	}
}

func TestApprovalEffectLabelsOnlyMapsDeclaredEffects(t *testing.T) {
	if got := approvalEffectLabels([]string{"workspace_write", "workspace_write", "unknown"}); got != "workspace write" {
		t.Fatalf("approvalEffectLabels() = %q, want %q", got, "workspace write")
	}
	if got := approvalEffectLabels(nil); got != "" {
		t.Fatalf("approvalEffectLabels(nil) = %q, want empty", got)
	}
}

func TestSafeSlackErrorCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "coded", err: fmt.Errorf("wrapped: %w", codedSlackStatusError("missing_scope")), want: "missing_scope"},
		{name: "deadline", err: context.DeadlineExceeded, want: "deadline_exceeded"},
		{name: "unknown", err: fmt.Errorf("secret body"), want: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := safeSlackErrorCode(test.err); got != test.want {
				t.Fatalf("safeSlackErrorCode() = %q, want %q", got, test.want)
			}
		})
	}
}

type connectionCardMessenger struct {
	blocks []map[string]any
}

func (m *connectionCardMessenger) PostMessage(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (m *connectionCardMessenger) PostMarkdownMessage(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (m *connectionCardMessenger) PostMessageBlocks(_ context.Context, _, _, _ string, blocks []map[string]any) (string, error) {
	m.blocks = blocks
	return "msg", nil
}

var _ slackconversation.Messenger = (*connectionCardMessenger)(nil)

func TestConnectionRequiredToolResultRendersChatAction(t *testing.T) {
	messenger := &connectionCardMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
	action := connections.ConnectionAction{Type: "connection", Provider: connections.ProviderClickStack, InstanceID: "i-prod", Label: "ClickStack", URL: "https://example.test/connect", State: "connect"}
	stream.Lifecycle(transcript.Event{
		Type:       transcript.ToolCallFailed,
		TurnID:     "turn-1",
		ToolCall:   &tool.Call{ID: "call-1", Name: "clickstack-query"},
		ToolResult: &tool.Result{ErrorCode: "connection_required", Metadata: map[string]any{"connection_action": action}, Content: []model.Content{{Type: model.ContentText, Text: "connect"}}},
	})
	if len(messenger.blocks) != 2 {
		t.Fatalf("expected connection card blocks, got %+v", messenger.blocks)
	}
	raw := fmt.Sprint(messenger.blocks)
	if !strings.Contains(raw, "https://example.test/connect") || !strings.Contains(raw, "Connect") {
		t.Fatalf("connection action was not rendered: %s", raw)
	}
}

func TestConfigureConnectionRequiredRendersProviderAddAction(t *testing.T) {
	messenger := &connectionCardMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
	action := connections.ConnectionAction{Type: "connection", Provider: connections.ProviderClickStack, Label: "ClickStack", State: "configure"}
	stream.Lifecycle(transcript.Event{
		Type:       transcript.ToolCallFailed,
		TurnID:     "turn-2",
		ToolCall:   &tool.Call{ID: "call-2", Name: "clickstack_connections"},
		ToolResult: &tool.Result{ErrorCode: "connection_required", Metadata: map[string]any{"connection_action": action}, Content: []model.Content{{Type: model.ContentText, Text: "configure"}}},
	})
	raw := fmt.Sprint(messenger.blocks)
	if !strings.Contains(raw, "add_connection") || !strings.Contains(raw, connections.ProviderClickStack) {
		t.Fatalf("configure action did not render provider add button: %s", raw)
	}
	if strings.Contains(raw, "connection_open") {
		t.Fatalf("configure action unexpectedly rendered URL action: %s", raw)
	}
}

func TestChatConnectionActionCarriesExplicitReturnContext(t *testing.T) {
	messenger := &connectionCardMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{UserID: "U1", Channel: "C1", ThreadTS: "171.1"})
	stream.connections = &connections.Service{Config: connections.Config{PublicBaseURL: "https://example.test", SecretKey: "secret"}}
	action := connections.ConnectionAction{Type: "connection", Provider: connections.ProviderClickStack, Label: "ClickStack", State: "connect"}
	stream.Lifecycle(transcript.Event{
		Type:       transcript.ToolCallFailed,
		TurnID:     "turn-chat",
		ToolCall:   &tool.Call{ID: "call-chat", Name: "clickstack-query"},
		ToolResult: &tool.Result{ErrorCode: "connection_required", Metadata: map[string]any{"connection_action": action}, Content: []model.Content{{Type: model.ContentText, Text: "connect"}}},
	})
	raw := fmt.Sprint(messenger.blocks)
	if !strings.Contains(raw, "origin=chat") || !strings.Contains(raw, "return_context=") {
		t.Fatalf("chat connection action omitted explicit return context: %s", raw)
	}
}

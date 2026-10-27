package slacktool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/connections"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

type canvasRecorder struct {
	standalone, channel int
	destination         string
}

func (c *canvasRecorder) CreateCanvas(context.Context, string, *slack.CanvasContent) (string, error) {
	c.standalone++
	return "F-canvas", nil
}
func (c *canvasRecorder) CreateChannelCanvas(_ context.Context, channel, _ string, _ *slack.CanvasContent) (string, error) {
	c.channel++
	c.destination = channel
	return "F-canvas", nil
}

func TestCanvasAudienceIsExplicitAndConnectedFailuresNeverUseBot(t *testing.T) {
	for _, channel := range []string{"", "C-explicit"} {
		bot := &canvasRecorder{}
		arguments, _ := json.Marshal(map[string]string{"title": "Evidence", "markdown": "[source](https://example.test)", "channel_id": channel})
		result, err := (CreateCanvasTool{Slack: bot}).Execute(t.Context(), tool.Call{Arguments: arguments, Scope: tool.Scope{Values: map[string]string{"channel": "C-current"}}})
		if err != nil || !strings.Contains(string(result.Content[0].JSON), "F-canvas") {
			t.Fatalf("receipt=%+v err=%v", result, err)
		}
		if channel == "" && (bot.standalone != 1 || bot.channel != 0) {
			t.Fatal("current conversation became implicit publication audience")
		}
		if channel != "" && (bot.standalone != 0 || bot.channel != 1 || bot.destination != channel) {
			t.Fatal("explicit channel was changed")
		}
	}
	bot := &canvasRecorder{}
	source := &recordingSearchSource{err: &connections.RequiredError{Provider: connections.ProviderSlack}}
	result, err := (CreateCanvasTool{Slack: bot, Source: source}).Execute(t.Context(), tool.Call{Arguments: json.RawMessage(`{"title":"Evidence","markdown":"text"}`), Scope: tool.Scope{UserID: "U-actor"}})
	if err != nil || result.ErrorCode != "connection_required" || bot.standalone+bot.channel != 0 || len(source.calls) != 1 || source.calls[0].Scope.UserID != "U-actor" {
		t.Fatalf("connection failure expanded to bot: %+v %v", result, err)
	}
}

func TestListToolsRetainTruncationUncertaintyAndActor(t *testing.T) {
	requests := 0
	source := &recordingSearchSource{client: slack.NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Path == "/api/slackLists.items.list" {
			return jsonResp(`{"ok":true,"list":{"list_metadata":{"schema":[]}},"items":[],"response_metadata":{"next_cursor":"more"}}`), nil
		}
		return jsonResp(`{"ok":false,"error":"internal_error"}`), nil
	}))}
	call := tool.Call{Arguments: json.RawMessage(`{"list_id":"F-list"}`), Scope: tool.Scope{UserID: "U-actor", SessionID: "session"}}
	result, err := (UserListItemsTool{Source: source}).Execute(t.Context(), call)
	if err != nil || !result.Truncated || source.calls[0].Scope.UserID != "U-actor" {
		t.Fatalf("read=%+v err=%v", result, err)
	}
	call.Arguments = json.RawMessage(`{"list_id":"F-list","initial_fields":[{"column_id":"Col1","checkbox":true}]}`)
	result, err = (UserCreateListItemTool{Source: source}).Execute(t.Context(), call)
	if err != nil || !result.IsError || result.ErrorCode != "uncertain_delivery" || result.Retryable || requests != 2 {
		t.Fatalf("write=%+v requests=%d err=%v", result, requests, err)
	}
	source.calls = nil
	call.Arguments = json.RawMessage(`{"list_id":"F-list","initial_fields":[{"column_id":"Col1"}]}`)
	if _, err := (UserCreateListItemTool{Source: source}).Execute(t.Context(), call); err == nil || len(source.calls) != 0 {
		t.Fatal("invalid native mapping resolved credentials")
	}
}

type taskSnapshotRecorder struct {
	calls []tool.Call
	err   error
}

func (s *taskSnapshotRecorder) Snapshot(_ context.Context, call tool.Call, link string) (slack.WorkObject, error) {
	s.calls = append(s.calls, call)
	if s.err != nil {
		return slack.WorkObject{}, s.err
	}
	return slack.WorkObject{AppUnfurlURL: link, URL: "https://github.com/org/repo/issues/42", ExternalRef: slack.WorkObjectRef{ID: "node-42", Type: "github_issue"}, EntityType: "slack#/entities/task", EntityPayload: slack.WorkObjectPayload{Attributes: slack.WorkObjectAttributes{Title: slack.WorkObjectTitle{Text: "Canonical task"}, MetadataLastModified: 123}, Fields: map[string]slack.WorkObjectField{"status": {Value: "open"}}}}, nil
}

func TestWorkObjectPublishesOnlyAuthorizedSourceSnapshot(t *testing.T) {
	writes := 0
	source := &recordingSearchSource{client: slack.NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		writes++
		var in struct {
			Channel  string `json:"channel"`
			TS       string `json:"ts"`
			Metadata struct {
				Entities []slack.WorkObject `json:"entities"`
			} `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path != "/api/chat.unfurl" || in.Channel != "C-explicit" || in.TS != "123.0001" || len(in.Metadata.Entities) != 1 {
			t.Fatal(in, r.URL)
		}
		object := in.Metadata.Entities[0]
		if object.EntityPayload.Fields["status"].Value != "open" || object.EntityPayload.Attributes.Title.Text != "Canonical task" || object.ExternalRef.ID != "node-42" {
			t.Fatal("model arguments changed canonical metadata", object)
		}
		return jsonResp(`{"ok":true}`), nil
	}))}
	tasks := &taskSnapshotRecorder{}
	item := UserUnfurlTaskTool{Slack: source, Tasks: tasks}
	call := tool.Call{Arguments: json.RawMessage(`{"url":"https://github.com/org/repo/issues/42","channel":"C-explicit","message_ts":"123.0001","status":"fake-complete"}`), Scope: tool.Scope{UserID: "U-actor", SessionID: "session", Values: map[string]string{"channel": "C-other"}}}
	if _, err := item.Execute(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	if writes != 1 || len(tasks.calls) != 1 || tasks.calls[0].Scope.UserID != "U-actor" {
		t.Fatal("task source lost actor scope")
	}
	tasks.err = &connections.RequiredError{Provider: connections.ProviderGitHub}
	result, err := item.Execute(t.Context(), call)
	if err != nil || result.ErrorCode != "connection_required" || writes != 1 {
		t.Fatalf("missing task authorization published: %+v %v", result, err)
	}
	call.Arguments = json.RawMessage(`{"url":"https://github.com/org/repo/issues/42","message_ts":"123.0001"}`)
	before := len(tasks.calls)
	if _, err := item.Execute(t.Context(), call); err == nil || len(tasks.calls) != before || writes != 1 {
		t.Fatal("destination inferred from conversation")
	}
}

func TestCompositionWriteDescriptorsKeepExactAuthorization(t *testing.T) {
	for _, item := range []tool.Tool{UserCreateListItemTool{}, UserUnfurlTaskTool{}, CreateCanvasTool{}} {
		descriptor := item.Descriptor()
		write := false
		for _, effect := range descriptor.Effects {
			if effect == tool.EffectExternalWrite {
				write = true
			}
		}
		if !write || len(descriptor.Surfaces) != 1 || descriptor.Surfaces[0] != "slack" {
			t.Fatalf("lost write/surface policy: %s", descriptor.Name)
		}
		if descriptor.Parallel {
			t.Fatal(fmt.Sprintf("write tool %s is parallel-safe", descriptor.Name))
		}
	}
}

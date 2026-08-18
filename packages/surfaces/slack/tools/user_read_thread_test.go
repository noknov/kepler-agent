package slacktool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

func TestUserReadThreadFormatsMessages(t *testing.T) {
	reader := &fakeThreadReader{
		messages: []slack.Message{
			{User: "U1", TS: "100.001", Text: "hello <@U2>"},
			{User: "U2", TS: "100.002", Text: "reply"},
		},
	}
	source := stubThreadReaderSource{reader: reader}
	result, err := (UserReadThreadTool{Source: source}).Execute(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"channel":"C123","thread_ts":"100.001","limit":10}`),
		Scope:     tool.Scope{UserID: "U123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "channel: C123") || !strings.Contains(result.Text(), "hello @U2") || !strings.Contains(result.Text(), "reply") {
		t.Fatalf("unexpected result: %q", result.Text())
	}
}

func TestUserReadThreadDefaultsToScope(t *testing.T) {
	reader := &fakeThreadReader{messages: []slack.Message{{User: "U1", TS: "1.0", Text: "scoped"}}}
	source := stubThreadReaderSource{reader: reader}
	_, err := (UserReadThreadTool{Source: source}).Execute(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{}`),
		Scope: tool.Scope{
			UserID: "U123",
			Values: map[string]string{"channel": "D9", "thread_ts": "9.9", "message_ts": "9.9"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reader.useHistory || reader.channel != "D9" || reader.latest != "9.9" {
		t.Fatalf("unexpected reader call: history=%v channel=%q latest=%q", reader.useHistory, reader.channel, reader.latest)
	}
}

func TestUserReadThreadExplicitChannelIgnoresScope(t *testing.T) {
	reader := &fakeThreadReader{messages: []slack.Message{{User: "U1", TS: "1.0", Text: "dm"}}}
	source := stubThreadReaderSource{reader: reader}
	_, err := (UserReadThreadTool{Source: source}).Execute(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"channel":"D0AJSE6PRLH"}`),
		Scope: tool.Scope{
			UserID: "U123",
			Values: map[string]string{"channel": "D_BOT", "thread_ts": "9.9", "message_ts": "9.9"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reader.useHistory || reader.channel != "D0AJSE6PRLH" || reader.latest != "" {
		t.Fatalf("unexpected reader call: history=%v channel=%q latest=%q", reader.useHistory, reader.channel, reader.latest)
	}
}

func (f *fakeThreadReader) ResolveReadTarget(_ context.Context, in slack.ReadTargetInput) (slack.ReadTarget, error) {
	channel := slack.NormalizeChannelRef(in.Channel)
	explicit := strings.TrimSpace(in.User) != "" || strings.TrimSpace(in.Link) != "" || (channel != "" && channel != strings.TrimSpace(in.ScopeChannel))
	if strings.TrimSpace(in.User) != "" {
		channel = "D_USER"
	}
	if channel == "" {
		channel = strings.TrimSpace(in.ScopeChannel)
	}
	threadTS := strings.TrimSpace(in.ThreadTS)
	latestTS := ""
	if !explicit {
		if threadTS == "" {
			threadTS = strings.TrimSpace(in.ScopeThreadTS)
		}
		latestTS = strings.TrimSpace(in.ScopeMessageTS)
	}
	return slack.ReadTarget{Channel: channel, ThreadTS: threadTS, LatestTS: latestTS}, nil
}

type fakeThreadReader struct {
	channel, threadTS, latest string
	limit                     int
	useHistory                bool
	messages                  []slack.Message
}

func (f *fakeThreadReader) Replies(_ context.Context, channel, threadTS string, limit int) ([]slack.Message, error) {
	f.useHistory = false
	f.channel = channel
	f.threadTS = threadTS
	f.limit = limit
	return f.messages, nil
}

func (f *fakeThreadReader) History(_ context.Context, channel, latest string, limit int) ([]slack.Message, error) {
	f.useHistory = true
	f.channel = channel
	f.latest = latest
	f.limit = limit
	return f.messages, nil
}

type stubThreadReaderSource struct {
	reader *fakeThreadReader
}

func (s stubThreadReaderSource) ThreadReader(context.Context, tool.Call) (ThreadReader, error) {
	return s.reader, nil
}

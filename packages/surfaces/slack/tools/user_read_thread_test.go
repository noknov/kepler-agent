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
			Values: map[string]string{"channel": "C9", "thread_ts": "9.9"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reader.channel != "C9" || reader.threadTS != "9.9" {
		t.Fatalf("unexpected reader call: channel=%q thread_ts=%q", reader.channel, reader.threadTS)
	}
}

type fakeThreadReader struct {
	channel, threadTS string
	limit             int
	messages          []slack.Message
}

func (f *fakeThreadReader) Replies(_ context.Context, channel, threadTS string, limit int) ([]slack.Message, error) {
	f.channel = channel
	f.threadTS = threadTS
	f.limit = limit
	return f.messages, nil
}

type stubThreadReaderSource struct {
	reader *fakeThreadReader
}

func (s stubThreadReaderSource) ThreadReader(context.Context, tool.Call) (ThreadReader, error) {
	return s.reader, nil
}

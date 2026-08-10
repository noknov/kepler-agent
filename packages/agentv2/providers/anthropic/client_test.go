package anthropic

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestGenerateMessagesStream(t *testing.T) {
	client := &Client{BaseURL: "https://gateway.test", APIKey: "secret", HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://gateway.test/v1/messages" {
			t.Fatalf("url = %s", request.URL)
		}
		stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":9,\"cache_read_input_tokens\":2}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n"
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream)), Header: make(http.Header)}, nil
	})}}
	response, err := client.Generate(context.Background(), model.Request{Model: "controlled", Messages: []model.Message{model.TextMessage(model.RoleSystem, "system"), model.TextMessage(model.RoleUser, "hi")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != "m1" || response.Message.Text() != "done" || response.Usage.InputTokens != 9 || response.Usage.OutputTokens != 2 {
		t.Fatalf("response=%+v", response)
	}
}

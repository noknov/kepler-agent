package openaichat

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

func TestGenerateStreamsTextToolAndUsage(t *testing.T) {
	var body string
	client := &Client{BaseURL: "https://gateway.test/v1", APIKey: "secret", HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(request.Body)
		body = string(data)
		if request.URL.String() != "https://gateway.test/v1/chat/completions" {
			t.Fatalf("url = %s", request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("missing auth")
		}
		stream := "data: {\"id\":\"r1\",\"choices\":[{\"delta\":{\"content\":\"hi \"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"search\",\"arguments\":\"{\\\"query\\\":\\\"x\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"prompt_tokens_details\":{\"cached_tokens\":4}}}\n\n" +
			"data: [DONE]\n\n"
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream)), Header: make(http.Header)}, nil
	})}}
	var events []model.StreamEvent
	response, err := client.Generate(context.Background(), model.Request{Model: "controlled", Messages: []model.Message{model.TextMessage(model.RoleUser, "hello")}, Tools: []model.ToolDefinition{{Name: "search", InputSchema: []byte(`{"type":"object"}`)}}}, func(event model.StreamEvent) error { events = append(events, event); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"stream":true`) || response.Message.Text() != "hi " {
		t.Fatalf("body=%s response=%+v", body, response)
	}
	calls := response.Message.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "search" || string(calls[0].Arguments) != `{"query":"x"}` {
		t.Fatalf("calls=%+v", calls)
	}
	if response.Usage.CacheReadTokens != 4 || len(events) < 4 {
		t.Fatalf("usage=%+v events=%+v", response.Usage, events)
	}
}

func TestEncodeMessagesPreservesImages(t *testing.T) {
	messages := encodeMessages([]model.Message{{Role: model.RoleUser, Content: []model.Content{
		{Type: model.ContentText, Text: "inspect"},
		{Type: model.ContentImage, ImageURL: "https://example.test/image.png"},
	}}})
	parts, ok := messages[0].Content.([]map[string]any)
	if !ok || len(parts) != 2 || parts[1]["type"] != "image_url" {
		t.Fatalf("encoded content = %#v", messages[0].Content)
	}
}

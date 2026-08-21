package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIResponsesClientOmitsUnsetTemperature(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`))
	}))
	defer server.Close()

	client := NewOpenAIResponsesClient("opencode-go", server.URL, "token", 0)
	if _, err := client.Chat(context.Background(), Request{
		Model:    "gpt-5.6-luna",
		Messages: []Message{{Role: "user", Content: "look"}},
	}); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if _, ok := gotBody["temperature"]; ok {
		t.Fatalf("temperature = %v, want omitted when unset", gotBody["temperature"])
	}
}

func TestOpenAIResponsesClientPostsResponsesBody(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if auth := r.Header.Get("Authorization"); auth != "Bearer token" {
			t.Fatalf("Authorization = %q, want Bearer token", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done","annotations":[{"type":"url_citation","url":"https://example.test/source","title":"Primary source","start_index":0,"end_index":4}]}]}],
			"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"cached_tokens":4}}
		}`))
	}))
	defer server.Close()

	client := NewOpenAIResponsesClient("opencode-go", server.URL, "token", 0)
	resp, err := client.Chat(context.Background(), Request{
		Model: "gpt-5.6-luna",
		Messages: []Message{
			{Role: "system", Content: "be concise"},
			{Role: "user", ContentParts: []ContentPart{TextPart("look"), ImageURLPart("https://example.test/image.png")}},
		},
		Tools: []ToolSpec{{
			Type: "function",
			Function: ToolSpecFunction{
				Name:        "repo-search",
				Description: "Search files",
				Parameters:  map[string]any{"type": "object"},
			},
		}},
		ToolChoice:  "auto",
		MaxTokens:   123,
		Temperature: float64Ptr(0.2),
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if gotPath != "/responses" {
		t.Fatalf("path = %q, want /responses", gotPath)
	}
	if resp.Message.Content != "done" {
		t.Fatalf("content = %q, want done", resp.Message.Content)
	}
	if len(resp.Message.Citations) != 1 || resp.Message.Citations[0].URL != "https://example.test/source" {
		t.Fatalf("citations = %#v", resp.Message.Citations)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CacheReadInputTokens != 4 || !resp.Usage.CacheIncludedInPrompt {
		t.Fatalf("usage = %#v", resp.Usage)
	}
	if gotBody["model"] != "gpt-5.6-luna" {
		t.Fatalf("model = %v", gotBody["model"])
	}
	if gotBody["max_output_tokens"].(float64) != 123 {
		t.Fatalf("max_output_tokens = %v", gotBody["max_output_tokens"])
	}
	if gotBody["temperature"].(float64) != 0.2 {
		t.Fatalf("temperature = %v, want 0.2", gotBody["temperature"])
	}
	input := gotBody["input"].([]any)
	user := input[1].(map[string]any)
	content := user["content"].([]any)
	if content[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("second content part = %#v, want input_image", content[1])
	}
	tools := gotBody["tools"].([]any)
	if tools[0].(map[string]any)["name"] != "repo-search" {
		t.Fatalf("tool = %#v", tools[0])
	}
}

func TestOpenAIResponsesClientParsesFunctionCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"completed",
			"output":[{"type":"function_call","call_id":"call_1","name":"code-search","arguments":"{\"query\":\"x\"}"}],
			"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}
		}`))
	}))
	defer server.Close()

	client := NewOpenAIResponsesClient("opencode-go", server.URL, "token", 0)
	resp, err := client.Chat(context.Background(), Request{Model: "gpt-5.6-luna", Messages: []Message{{Role: "user", Content: "search"}}})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %#v, want one", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Function.Name != "code-search" || call.Function.Arguments != `{"query":"x"}` {
		t.Fatalf("call = %#v", call)
	}
}

func TestOpenAIResponsesClientStreamPreservesFunctionCallID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_tmp_1","type":"function_call","status":"in_progress","name":"echo","call_id":"call_1","arguments":""}}`,
			`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_tmp_1","arguments":"{\"text\":\"hello\"}","name":"echo"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_tmp_1","type":"function_call","status":"completed","name":"echo","call_id":"call_1","arguments":"{\"text\":\"hello\"}"}}`,
			`{"type":"response.completed","response":{"status":"completed","output":[{"id":"fc_tmp_1","type":"function_call","status":"completed","name":"echo","call_id":"call_1","arguments":"{\"text\":\"hello\"}"}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}}`,
		}
		for _, event := range events {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()

	var streamed []ToolCall
	client := NewOpenAIResponsesClient("opencode-go", server.URL, "token", 0)
	resp, err := client.ChatStream(context.Background(), Request{Model: "gpt-5.6-luna", Messages: []Message{{Role: "user", Content: "echo"}}}, StreamHandler{
		OnToolCallComplete: func(call ToolCall) { streamed = append(streamed, call) },
	})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %#v, want one", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Function.Name != "echo" || call.Function.Arguments != `{"text":"hello"}` {
		t.Fatalf("call = %#v", call)
	}
	if len(streamed) != 1 || streamed[0].ID != "call_1" {
		t.Fatalf("streamed calls = %#v, want one call with preserved ID", streamed)
	}
}

func TestResponsesInputSkipsToolHistoryWithoutCallID(t *testing.T) {
	input := responsesInput([]Message{
		{Role: "assistant", ToolCalls: []ToolCall{{Type: "function", Function: ToolFunction{Name: "broken", Arguments: `{}`}}}},
		{Role: "tool", Name: "broken", Content: "result"},
		{Role: "user", Content: "continue"},
	})

	if len(input) != 1 {
		t.Fatalf("input = %#v, want only the valid user message", input)
	}
	message := input[0].(map[string]any)
	if message["role"] != "user" {
		t.Fatalf("message = %#v, want user message", message)
	}
}

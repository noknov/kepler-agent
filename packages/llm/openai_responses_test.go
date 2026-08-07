package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],
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
		Temperature: 0.2,
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
	if resp.Usage.PromptTokens != 10 || resp.Usage.CacheReadInputTokens != 4 || !resp.Usage.CacheIncludedInPrompt {
		t.Fatalf("usage = %#v", resp.Usage)
	}
	if gotBody["model"] != "gpt-5.6-luna" {
		t.Fatalf("model = %v", gotBody["model"])
	}
	if gotBody["max_output_tokens"].(float64) != 123 {
		t.Fatalf("max_output_tokens = %v", gotBody["max_output_tokens"])
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

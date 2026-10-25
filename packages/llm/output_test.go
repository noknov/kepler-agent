package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNativeOutputWireContracts(t *testing.T) {
	for _, protocol := range []string{"openai", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var request map[string]json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(raw, &request); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				switch protocol {
				case "openai":
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}]}`)
				case "responses":
					io.WriteString(w, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{}"}]}]}`)
				case "anthropic":
					io.WriteString(w, `{"content":[{"type":"text","text":"{}"}],"stop_reason":"end_turn"}`)
				}
			}))
			defer server.Close()
			var client Client
			switch protocol {
			case "openai":
				client = NewOpenAICompatibleClient("test", server.URL, "", time.Second)
			case "responses":
				client = NewOpenAIResponsesClient("test", server.URL, "", time.Second)
			case "anthropic":
				client = NewAnthropicClient(server.URL, "", time.Second, "")
			}
			schema := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
			_, err := client.Chat(context.Background(), Request{Model: "test", OutputFormat: &OutputFormat{Name: "result", Schema: schema}})
			if err != nil {
				t.Fatal(err)
			}
			key := map[string]string{"openai": "response_format", "responses": "text", "anthropic": "output_config"}[protocol]
			var envelope map[string]json.RawMessage
			if json.Unmarshal(request[key], &envelope) != nil {
				t.Fatalf("missing %s: %s", key, request[key])
			}
			inner := envelope["format"]
			if protocol == "openai" {
				inner = envelope["json_schema"]
			}
			var format map[string]json.RawMessage
			if json.Unmarshal(inner, &format) != nil || string(format["schema"]) != string(schema) {
				t.Fatalf("invalid schema mapping: %s", inner)
			}
			if protocol != "anthropic" && string(format["strict"]) != "true" {
				t.Fatalf("native constraints not strict: %s", inner)
			}
		})
	}
}

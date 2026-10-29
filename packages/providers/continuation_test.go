package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/llm"
	"github.com/noknov/kepler-agent/packages/llm/chatgpt"
)

func TestNativeCompactionWindowRoundTripsAndFallsBackAcrossRoutes(t *testing.T) {
	raw := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"retained"}]},{"type":"compaction","id":"cmp_1","encrypted_content":"opaque"},{"type":"future_item","exact_id":9007199254740993}]`
	wire := llm.NewOpenAIResponsesClient("openai", "https://provider.example/v1", "key", 0)
	calls := 0
	wire.SetTransport(nativeQoderTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var request struct {
			Input json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		body := `{"output":` + raw + `,"usage":{"input_tokens":30,"output_tokens":10}}`
		if r.URL.Path == "/v1/responses" {
			var got, want any
			decode := func(data []byte, target *any) {
				d := json.NewDecoder(strings.NewReader(string(data)))
				d.UseNumber()
				if err := d.Decode(target); err != nil {
					t.Fatal(err)
				}
			}
			decode(request.Input, &got)
			decode([]byte(raw), &want)
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(want)
			if string(a) != string(b) {
				t.Fatalf("window changed: %s != %s", a, b)
			}
			body = `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":1}}`
		} else if r.URL.Path != "/v1/responses/compact" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	}))
	config := Config{Provider: "openai", Protocol: "responses", BaseURL: "https://provider.example/v1", APIKey: "key"}
	client := bindClient(wire, config)
	compact, err := client.Generate(context.Background(), model.Request{Operation: model.OperationCompact, Model: "model", Messages: []model.Message{model.TextMessage(model.RoleUser, "history")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := model.TextMessage(model.RoleUser, "portable checkpoint")
	checkpoint.Content = append(checkpoint.Content, compact.Message.Content...)
	if _, err := client.Generate(context.Background(), model.Request{Model: "model", Messages: []model.Message{checkpoint}}, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	otherConfig := config
	otherConfig.APIKey = "rotated"
	rotated := bindClient(&recordingWire{}, otherConfig)
	for _, test := range []struct {
		client *Client
		name   string
	}{{rotated, "model"}, {client, "other-model"}} {
		converted, err := test.client.inputMessage(checkpoint, test.name)
		if err != nil || len(converted.NativeItems) != 0 || converted.Content != "portable checkpoint" {
			t.Fatalf("unsafe fallback: %+v %v", converted, err)
		}
	}
	// The original response is immutable, so switching providers can still replay it later.
	if !strings.Contains(string(checkpoint.Content[1].Continuation.Items), "future_item") {
		t.Fatal("unknown native item was lost")
	}
}

func TestResponsesReasoningAndUnknownItemsRemainOpaque(t *testing.T) {
	raw := json.RawMessage(`{"type":"reasoning","id":"r","encrypted_content":"private"}`)
	wire := &recordingWire{response: llm.Response{Message: llm.Message{Role: "assistant", Content: "answer", NativeItems: []json.RawMessage{raw}}, FinishReason: "completed"}}
	client := bindClient(wire, Config{Provider: "openai", Protocol: "responses", BaseURL: "https://example"})
	result, err := client.Generate(context.Background(), model.Request{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := client.inputMessage(result.Message, "m")
	if err != nil || len(converted.NativeItems) != 1 || string(converted.NativeItems[0]) != string(raw) {
		t.Fatalf("lost opaque reasoning: %+v %v", converted, err)
	}
}

func TestContinuationFallsBackWhenProtocolRouteChanges(t *testing.T) {
	config := Config{Provider: "compatible", Protocol: "openai", BaseURL: "https://example"}
	router := llm.NewProtocolRouter(&recordingWire{}, streamingWire{}, nil)
	client := bindClient(router, config)
	state := &model.Continuation{Version: 1, Provider: client.provider, Model: "model", Route: client.route, Kind: "compaction", Items: json.RawMessage(`[{"type":"compaction","encrypted_content":"private"}]`)}
	message := model.TextMessage(model.RoleUser, "portable checkpoint")
	message.Content = append(message.Content, model.Content{Type: model.ContentContinuation, Continuation: state})
	converted, err := client.inputMessage(message, "model")
	if err != nil || len(converted.NativeItems) != 0 || converted.Content != "portable checkpoint" {
		t.Fatalf("Chat route silently lost the portable window: %+v %v", converted, err)
	}
}

func TestChatGPTContinuationUsesAccountIdentityNotCredentialPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	credentials := chatgpt.Session{ClientID: "client", HostID: "host", Subject: "alice", AccessToken: "access", RefreshToken: "refresh", Scope: "chatgpt.tokens.use.direct resource.invoke", ExpiresAt: time.Now().Add(time.Hour)}
	write := func() {
		t.Helper()
		raw, _ := json.Marshal(credentials)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	client := bindClient(&recordingWire{}, Config{Provider: "chatgpt", Protocol: "responses", ChatGPTCredentialsFile: path})
	alice, _, err := client.bindSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	message := model.TextMessage(model.RoleAssistant, "portable answer")
	message.Content = append(message.Content, model.Content{Type: model.ContentContinuation, Continuation: alice.continuation(llm.Message{NativeItems: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}}, "m", "response", llm.Usage{})})
	credentials.AccessToken = "rotated"
	write()
	refreshed, _, _ := client.bindSession(context.Background())
	converted, err := refreshed.inputMessage(message, "m")
	if err != nil || len(converted.NativeItems) != 1 {
		t.Fatal("normal refresh lost continuation")
	}
	credentials.Subject = "bob"
	write()
	bob, _, err := client.bindSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	converted, err = bob.inputMessage(message, "m")
	if err != nil || len(converted.NativeItems) != 0 || converted.Content != "portable answer" {
		t.Fatal("relogin reused another account's opaque continuation")
	}
	if alice.route == bob.route || client.route == alice.route {
		t.Fatal("shared client routing was mutated")
	}
}

func TestIncompleteResponseDoesNotReplaySuppressedNativeCalls(t *testing.T) {
	wire := llm.NewOpenAIResponsesClient("openai", "https://provider.example/v1", "key", 0)
	calls := 0
	wire.SetTransport(nativeQoderTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var request struct {
			Input json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		body := `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"reasoning","id":"reasoning","encrypted_content":"opaque"},{"type":"function_call","id":"fc","call_id":"call","name":"write","arguments":"{"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial answer"}]}],"usage":{"input_tokens":4,"output_tokens":9}}`
		if calls == 2 {
			if strings.Contains(string(request.Input), `"function_call"`) || strings.Contains(string(request.Input), `"opaque"`) || !strings.Contains(string(request.Input), "partial answer") {
				t.Fatalf("suppressed call leaked into next request: %s", request.Input)
			}
			body = `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	client := bindClient(wire, Config{Provider: "openai", Protocol: "responses", BaseURL: "https://provider.example/v1", APIKey: "key"})
	response, err := client.Generate(t.Context(), model.Request{Model: "model"}, nil)
	if err != nil || response.FinishReason != model.FinishLength || len(response.Message.ToolCalls()) != 0 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	_, err = client.Generate(t.Context(), model.Request{Model: "model", Messages: []model.Message{response.Message, model.TextMessage(model.RoleUser, "continue")}}, nil)
	if err != nil || calls != 2 {
		t.Fatalf("follow-up calls=%d err=%v", calls, err)
	}
}

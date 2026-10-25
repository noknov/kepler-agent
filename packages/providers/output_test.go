package providers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/llm"
)

func TestStructuredOutputValidatesBeforePublication(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer","minimum":9007199254740993,"maximum":9007199254740993}},"required":["value"],"additionalProperties":false}`)
	for _, tc := range []struct {
		name, text string
		finish     string
		valid      bool
	}{
		{"exact large integer", `{"value":9007199254740993}`, "stop", true},
		{"precision loss", `{"value":9007199254740992}`, "stop", false},
		{"extra field", `{"value":9007199254740993,"secret":"DO_NOT_LOG"}`, "stop", false},
		{"wrong type", `{"value":"DO_NOT_LOG"}`, "stop", false},
		{"missing", `{}`, "stop", false},
		{"trailing", `{"value":9007199254740993} {}`, "stop", false},
		{"incomplete valid JSON", `{"value":9007199254740993}`, "length", false},
		{"refusal", `DO_NOT_LOG`, "content_filter", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := &recordingWire{response: llm.Response{Message: llm.Message{Role: "assistant", Content: tc.text}, FinishReason: tc.finish, Usage: llm.Usage{Reported: true, PromptTokens: 7}}}
			client := &Client{Wire: wire}
			var events []model.StreamEvent
			response, err := client.Generate(context.Background(), model.Request{OutputFormat: &model.OutputFormat{Name: "result", Schema: schema}}, func(e model.StreamEvent) error { events = append(events, e); return nil })
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if response.Usage.InputTokens != 7 {
				t.Fatalf("usage lost: %#v", response.Usage)
			}
			if tc.valid {
				if len(events) != 2 || events[0].Text != tc.text || wire.request.OutputFormat == nil {
					t.Fatalf("events=%v request=%#v", events, wire.request)
				}
			} else if len(events) != 0 || response.Message.Text() != "" || len(response.RawMetadata) != 0 || strings.Contains(err.Error(), "DO_NOT_LOG") {
				t.Fatalf("unvalidated content escaped: %#v %v", response, err)
			}
		})
	}
}

func TestStructuredOutputRejectsInvalidContractBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format model.OutputFormat
	}{
		{"missing schema", model.OutputFormat{Name: "x"}},
		{"invalid schema", model.OutputFormat{Name: "x", Schema: json.RawMessage(`{"type":"nonsense"}`)}},
		{"external reference", model.OutputFormat{Name: "x", Schema: json.RawMessage(`{"$ref":"file:///etc/passwd"}`)}},
		{"remote reference", model.OutputFormat{Name: "x", Schema: json.RawMessage(`{"$ref":"https://example.invalid/private"}`)}},
		{"mode", model.OutputFormat{Name: "x", Schema: json.RawMessage(`{}`), Mode: "guess"}},
		{"name", model.OutputFormat{Name: "bad name", Schema: json.RawMessage(`{}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := &recordingWire{}
			_, err := (&Client{Wire: wire}).Generate(context.Background(), model.Request{Model: "not-dispatched", OutputFormat: &tc.format}, nil)
			if model.ErrorKindOf(err) != model.ErrorInvalid || wire.request.Model != "" {
				t.Fatalf("request=%#v err=%v", wire.request, err)
			}
		})
	}
}

func TestStructuredOutputInternalReferencesAndExplicitValidatedMode(t *testing.T) {
	wire := &recordingWire{response: llm.Response{Message: llm.Message{Role: "assistant", Content: `{"n":1}`}, FinishReason: "stop"}}
	format := &model.OutputFormat{Name: "result", Mode: model.OutputValidated, Schema: json.RawMessage(`{"$defs":{"number":{"type":"integer"}},"type":"object","properties":{"n":{"$ref":"#/$defs/number"}},"required":["n"]}`)}
	_, err := (&Client{Wire: wire}).Generate(context.Background(), model.Request{OutputFormat: format}, nil)
	if err != nil || wire.request.OutputFormat != nil {
		t.Fatalf("native constraint leaked: %#v %v", wire.request, err)
	}
	_, err = (&Client{Wire: wire}).Generate(context.Background(), model.Request{OutputFormat: format, Tools: []model.ToolDefinition{{Name: "write"}}}, nil)
	if model.ErrorKindOf(err) != model.ErrorInvalid {
		t.Fatalf("tool/structured ambiguity accepted: %v", err)
	}
}

func TestNativeOutputUnsupportedDoesNotDowngrade(t *testing.T) {
	client := &Client{Wire: &llm.QoderClient{}}
	_, err := client.Generate(context.Background(), model.Request{OutputFormat: &model.OutputFormat{Name: "result", Schema: json.RawMessage(`{"type":"object"}`)}}, nil)
	if model.ErrorKindOf(err) != model.ErrorUnsupported {
		t.Fatalf("unsupported native protocol: %v", err)
	}
}

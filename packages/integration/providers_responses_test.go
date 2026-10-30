package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/providers"
)

func TestResponsesTerminalReceiptsDoNotTriggerEmptyRecovery(t *testing.T) {
	for _, reason := range []string{"max_output_tokens", "content_filter"} {
		t.Run(reason, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":%q},\"output\":[],\"usage\":{\"input_tokens\":4,\"output_tokens\":9}}}\n\n", reason)
			}))
			defer server.Close()
			client, err := providers.New(providers.Config{Provider: "openai", Protocol: "responses", BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			catalog, _ := tool.NewCatalog()
			runner, err := agentruntime.New(agentruntime.Config{Model: "test", MaxEmptyResponseRetries: 2}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.RunTurn(t.Context(), agentruntime.TurnRequest{SessionID: "responses-" + reason, Input: model.TextMessage(model.RoleUser, "inspect")})
			if reason == "content_filter" {
				var typed *model.Error
				if !errors.As(err, &typed) || typed.Kind != model.ErrorContentPolicy {
					t.Fatalf("lost content refusal: %v", err)
				}
			} else if err != nil || result.Termination != agentruntime.TerminationOutputLimit {
				t.Fatalf("lost output limit: %+v err=%v", result, err)
			}
			if requests != 1 || result.Usage.InputTokens != 4 || result.Usage.OutputTokens != 9 {
				t.Fatalf("terminal request retried or usage lost: requests=%d result=%+v", requests, result)
			}
		})
	}
}

type roundTripEcho struct{}

func (roundTripEcho) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []tool.Effect{tool.EffectRead}}
}

func (roundTripEcho) Execute(context.Context, tool.Call) (tool.Result, error) {
	return tool.TextResult("echoed"), nil
}

func TestResponsesFactoryCompletesCanonicalToolRoundTrip(t *testing.T) {
	for _, sparse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sparse=%v", sparse), func(t *testing.T) { responsesToolRoundTrip(t, sparse, "") })
	}
}

func TestResponsesFactoryPreservesLargeSparseContinuation(t *testing.T) {
	responsesToolRoundTrip(t, true, strings.Repeat("A", 2<<20))
}

func responsesToolRoundTrip(t *testing.T, sparse bool, reasoning string) {
	t.Helper()
	requests := 0
	var secondInput []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			index := 0
			if reasoning != "" {
				fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"reasoning\",\"type\":\"reasoning\",\"encrypted_content\":%q}}\n\n", reasoning)
				index++
			}
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":{\"id\":\"fc_internal\",\"type\":\"function_call\",\"status\":\"completed\",\"name\":\"echo\",\"call_id\":\"call_public\",\"arguments\":\"{}\"}}\n\n", index)
			output := `[{"id":"fc_internal","type":"function_call","name":"echo","call_id":"call_public","arguments":"{}"}]`
			if sparse {
				output = `[]`
			}
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":%s}}\n\n", output)
			return
		}
		secondInput, _ = body["input"].([]any)
		item := `{"id":"answer","type":"message","phase":"final_answer","role":"assistant","content":[{"type":"output_text","text":"done"}]}`
		fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
		output := "[" + item + "]"
		if sparse {
			output = `[]`
		}
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":%s}}\n\n", output)
	}))
	defer server.Close()
	client, err := providers.New(providers.Config{Provider: "openai", Protocol: "responses", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := tool.NewCatalog(roundTripEcho{})
	cfg := agentruntime.Config{Model: "test"}
	if reasoning != "" {
		cfg.Context.MaxTokens = 1 << 20
	} // Account for the large opaque continuation in this fixture.
	runner, _ := agentruntime.New(cfg, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	result, err := runner.RunTurn(context.Background(), agentruntime.TurnRequest{SessionID: "responses", Input: model.TextMessage(model.RoleUser, "echo")})
	if err != nil || result.Message.Text() != "done" || requests != 2 {
		t.Fatalf("result=%+v requests=%d err=%v", result, requests, err)
	}
	var sawCall, sawOutput bool
	var returnedReasoning string
	for _, raw := range secondInput {
		item, _ := raw.(map[string]any)
		if item["type"] == "reasoning" {
			returnedReasoning, _ = item["encrypted_content"].(string)
		}
		if item["type"] == "function_call" && item["call_id"] == "call_public" {
			sawCall = true
		}
		if item["type"] == "function_call_output" && item["call_id"] == "call_public" {
			sawOutput = true
		}
	}
	if !sawCall || !sawOutput {
		t.Fatalf("second request omitted call/output: call=%v output=%v items=%d", sawCall, sawOutput, len(secondInput))
	}
	if returnedReasoning != reasoning {
		t.Fatalf("continuation changed: bytes=%d want=%d", len(returnedReasoning), len(reasoning))
	}
}

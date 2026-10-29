package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"fc_internal\",\"type\":\"function_call\",\"status\":\"completed\",\"name\":\"echo\",\"call_id\":\"call_public\",\"arguments\":\"{}\"}}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"id\":\"fc_internal\",\"type\":\"function_call\",\"name\":\"echo\",\"call_id\":\"call_public\",\"arguments\":\"{}\"}]}}\n\n")
			return
		}
		secondInput, _ = body["input"].([]any)
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\n")
	}))
	defer server.Close()
	client, err := providers.New(providers.Config{Provider: "openai", Protocol: "responses", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := tool.NewCatalog(roundTripEcho{})
	runner, _ := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	result, err := runner.RunTurn(context.Background(), agentruntime.TurnRequest{SessionID: "responses", Input: model.TextMessage(model.RoleUser, "echo")})
	if err != nil || result.Message.Text() != "done" || requests != 2 {
		t.Fatalf("result=%+v requests=%d err=%v", result, requests, err)
	}
	var sawCall, sawOutput bool
	for _, raw := range secondInput {
		item, _ := raw.(map[string]any)
		if item["type"] == "function_call" && item["call_id"] == "call_public" {
			sawCall = true
		}
		if item["type"] == "function_call_output" && item["call_id"] == "call_public" {
			sawOutput = true
		}
	}
	if !sawCall || !sawOutput {
		t.Fatalf("second request input=%#v", secondInput)
	}
}

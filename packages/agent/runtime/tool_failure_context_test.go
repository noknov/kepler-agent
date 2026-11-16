package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestToolFailureIsModelVisibleWithoutReplacingPartialEvidence(t *testing.T) {
	result := tool.TextResult("compiler produced partial diagnostics")
	result.IsError = true
	result.ErrorCode = "command_timeout"
	original := append([]model.Content(nil), result.Content...)
	catalog, _ := tool.NewCatalog(auditTool{name: "command", execute: func(context.Context) (tool.Result, error) { return result, nil }})
	call := model.ToolCall{ID: "call", Name: "command", Arguments: json.RawMessage(`{}`)}
	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}}, FinishReason: model.FinishToolCalls},
		{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop},
	}}
	store := transcript.NewMemoryStore()
	runner, err := New(Config{}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "compile")}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range client.requests[1].Messages {
		for _, block := range message.Content {
			if block.ToolResult == nil || block.ToolResult.CallID != call.ID {
				continue
			}
			content := block.ToolResult.Content
			if len(content) != 2 || content[0].Type != model.ContentJSON || content[1].Text != original[0].Text {
				t.Fatalf("failure status or original evidence lost: %+v", content)
			}
			var status map[string]string
			if err := json.Unmarshal(content[0].JSON, &status); err != nil {
				t.Fatal(err)
			}
			if status["tool_status"] != "error" || status["error_code"] != "command_timeout" {
				t.Fatalf("failure is ambiguous for text-only protocols: %+v", status)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("tool result missing from the next inference")
	}
	events, err := store.Load(t.Context(), "s", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == transcript.ToolCallFailed && !reflect.DeepEqual(event.ToolResult.Content, original) {
			t.Fatal("derived status mutated canonical evidence")
		}
	}
}

func TestStructuredToolFailureUsesNormalResultBudget(t *testing.T) {
	for _, mode := range []string{"inline", "spill", "truncate"} {
		t.Run(mode, func(t *testing.T) {
			text := strings.Repeat("diagnostic evidence\n", 180) + "successful sibling findings at the end"
			result := tool.TextResult(text)
			result.IsError, result.ErrorCode = true, "partial_failure"
			catalog, _ := tool.NewCatalog(auditTool{name: "inspect", execute: func(context.Context) (tool.Result, error) { return result, nil }})
			call := model.ToolCall{ID: "call", Name: "inspect", Arguments: json.RawMessage(`{}`)}
			client := &scriptedModel{responses: []model.Response{
				{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}}, FinishReason: model.FinishToolCalls},
				{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop},
			}}
			config := Config{ToolResults: ToolResultConfig{MaxInlineBytes: 8000}}
			store := transcript.NewMemoryStore()
			deps := Dependencies{Model: client, Tools: catalog, Transcript: store}
			artifacts := &artifactMemory{}
			if mode != "inline" {
				config.ToolResults.MaxInlineBytes = 2500
			}
			if mode == "spill" {
				deps.Artifacts = artifacts
			}
			runner, err := New(config, deps)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "inspect")}); err != nil {
				t.Fatal(err)
			}
			events, err := store.Load(t.Context(), "s", 0)
			if err != nil {
				t.Fatal(err)
			}
			var got *tool.Result
			for _, event := range events {
				if event.Type == transcript.ToolCallFailed {
					got = event.ToolResult
				}
			}
			if got == nil || !got.IsError || got.ErrorCode != "partial_failure" {
				t.Fatalf("lost failure status: %+v", got)
			}
			switch mode {
			case "inline":
				if got.Truncated || got.Content[0].Text != text {
					t.Fatal("structured error evidence was silently shortened")
				}
			case "spill":
				var content []model.Content
				if !got.Truncated || got.Spill == nil || json.Unmarshal(artifacts.content, &content) != nil || len(content) != 1 || content[0].Text != text {
					t.Fatal("full structured error evidence was not preserved in the artifact")
				}
			case "truncate":
				if !got.Truncated || got.Spill != nil || got.Content[0].Text != text[:2500] {
					t.Fatal("structured error bypassed the configured fallback budget")
				}
			}
		})
	}
}

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

func TestModelContextPreservesToolTruncationReceipt(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		content := make([]model.Content, 1, 4)
		content[0] = model.Content{Type: model.ContentText, Text: "source prefix"}
		original := append([]model.Content(nil), content...)
		result := tool.Result{Content: content, Truncated: truncated}
		event := transcript.Event{Type: transcript.ToolCallCompleted, ToolCall: &tool.Call{ID: "call", Name: "read"}, ToolResult: &result}
		first, ok := eventMessage(event)
		if !ok {
			t.Fatal("result not projected")
		}
		second, _ := eventMessage(event)
		projected := first.Content[0].ToolResult
		if projected.CallID != "call" || projected.Name != "read" || projected.Content[0].Text != "source prefix" {
			t.Fatalf("lost evidence/identity: %+v", projected)
		}
		expected := 1
		if truncated {
			expected = 2
		}
		if len(projected.Content) != expected || (truncated && !strings.Contains(projected.Content[1].Text, "incomplete")) {
			t.Fatalf("truncation receipt lost: %+v", projected)
		}
		if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(result.Content, original) || content[:cap(content)][1].Type != "" {
			t.Fatal("projection mutated durable evidence or accumulated notices")
		}
	}
}

func TestRunTurnPassesIncompleteEvidenceStatusToTheModel(t *testing.T) {
	for _, test := range []struct {
		name           string
		partial, spill bool
	}{
		{"source limited", true, false}, {"complete", false, false}, {"runtime spilled", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := tool.TextResult("source prefix that is available")
			result.Truncated = test.partial
			catalog, _ := tool.NewCatalog(auditTool{name: "source", execute: func(context.Context) (tool.Result, error) { return result, nil }})
			call := model.ToolCall{ID: "source-call", Name: "source", Arguments: json.RawMessage(`{}`)}
			client := &scriptedModel{responses: []model.Response{
				{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}}, FinishReason: model.FinishToolCalls},
				{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop},
			}}
			store := transcript.NewMemoryStore()
			deps := Dependencies{Model: client, Tools: catalog, Transcript: store}
			config := Config{Model: "test"}
			if test.spill {
				deps.Artifacts = &artifactMemory{}
				config.ToolResults = ToolResultConfig{MaxInlineBytes: 8}
			}
			runner, err := New(config, deps)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "read evidence")}); err != nil {
				t.Fatal(err)
			}
			if len(client.requests) != 2 {
				t.Fatalf("model calls=%d", len(client.requests))
			}
			found := false
			for _, message := range client.requests[1].Messages {
				for _, block := range message.Content {
					if block.ToolResult == nil || block.ToolResult.CallID != call.ID {
						continue
					}
					found = true
					var text string
					for _, part := range block.ToolResult.Content {
						text += part.Text
					}
					if strings.Contains(text, "incomplete") != (test.partial || test.spill) {
						t.Fatalf("model lost completeness status: %q", text)
					}
					if test.spill && !strings.Contains(text, "artifact://large") {
						t.Fatalf("lost artifact continuation: %q", text)
					}
				}
			}
			if !found {
				t.Fatal("tool evidence not sent to model")
			}
			events, err := store.Load(t.Context(), "s", 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.ToolResult == nil {
					continue
				}
				for _, part := range event.ToolResult.Content {
					if strings.Contains(part.Text, "incomplete") {
						t.Fatal("derived completeness notice was persisted as source evidence")
					}
				}
			}
		})
	}
}

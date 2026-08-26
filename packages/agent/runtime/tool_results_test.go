package runtime

import (
	"context"
	"testing"
	"unicode/utf8"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
)

type artifactMemory struct{ content []byte }

func (s *artifactMemory) Put(_ context.Context, _ tool.Scope, _ string, content []byte) (model.Artifact, error) {
	s.content = append([]byte(nil), content...)
	return model.Artifact{URI: "artifact://large", SizeBytes: int64(len(content))}, nil
}

func TestLimitToolResultSpillsLargePayload(t *testing.T) {
	store := &artifactMemory{}
	result := limitToolResult(context.Background(), tool.TextResult("a payload too large to inline"), tool.Call{ID: "1", Name: "read"}, ToolResultConfig{MaxInlineBytes: 8}, store)
	if !result.Truncated || result.Spill == nil || result.Spill.URI != "artifact://large" || len(store.content) == 0 {
		t.Fatalf("result=%+v stored=%q", result, store.content)
	}
	if len(result.Content) != 2 || result.Content[1].Type != model.ContentArtifact {
		t.Fatalf("content=%+v", result.Content)
	}
}

func TestLimitToolResultBatchAppliesAggregateBudget(t *testing.T) {
	store := &artifactMemory{}
	first, second := tool.TextResult("1234567890"), tool.TextResult("abcdefghij")
	prepared := []preparedCall{
		{call: tool.Call{ID: "1", Name: "read"}, result: &first},
		{call: tool.Call{ID: "2", Name: "read"}, result: &second},
	}
	limitToolResultBatch(context.Background(), prepared, ToolResultConfig{MaxInlineBytes: 100, MaxBatchBytes: 16}, store)
	if prepared[0].result.Spill == nil || prepared[1].result.Spill == nil {
		t.Fatalf("batch results were not spilled: %+v", prepared)
	}
}

func TestLimitToolResultFallbackKeepsUTF8AndValidContent(t *testing.T) {
	result := limitToolResult(context.Background(), tool.TextResult("你好世界"), tool.Call{ID: "1", Name: "read"}, ToolResultConfig{MaxInlineBytes: 5}, nil)
	if !result.Truncated || len(result.Content) < 1 || !utf8.ValidString(result.Content[0].Text) {
		t.Fatalf("result=%+v", result)
	}
}

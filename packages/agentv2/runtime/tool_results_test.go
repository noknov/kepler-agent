package runtime

import (
	"context"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
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

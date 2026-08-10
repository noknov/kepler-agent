package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

type PGArtifactStore struct{ Store registry.ToolSpillStore }

func (s PGArtifactStore) Put(ctx context.Context, scope tool.Scope, name string, content []byte) (model.Artifact, error) {
	if s.Store == nil {
		return model.Artifact{}, fmt.Errorf("artifact store is unavailable")
	}
	callID := strings.TrimSuffix(strings.TrimPrefix(name, "artifact-"), ".json")
	if err := s.Store.SaveToolSpill(ctx, scope.TurnID, "v2-artifact", callID, string(content)); err != nil {
		return model.Artifact{}, err
	}
	return model.Artifact{ID: callID, Name: name, MediaType: "application/json", URI: "spill://" + scope.TurnID + "/v2-artifact/" + callID, SizeBytes: int64(len(content))}, nil
}

type ArtifactReadTool struct{ Store registry.ToolSpillStore }

func (ArtifactReadTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "artifact_read", Description: "Read a large tool result referenced by a spill:// artifact URI.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"uri":{"type":"string"}},"required":["uri"]}`), Effects: []tool.Effect{tool.EffectRead}, Exposure: tool.ExposureDeferred}
}

func (t ArtifactReadTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if !strings.HasPrefix(args.URI, "spill://") {
		return tool.Result{}, fmt.Errorf("invalid artifact URI")
	}
	parts := strings.Split(strings.TrimPrefix(args.URI, "spill://"), "/")
	if len(parts) != 3 {
		return tool.Result{}, fmt.Errorf("invalid artifact URI")
	}
	content, err := t.Store.ReadToolSpill(ctx, parts[0], parts[1], parts[2])
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(content), nil
}

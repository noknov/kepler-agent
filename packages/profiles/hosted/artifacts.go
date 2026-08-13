package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type ToolSpillStore interface {
	SaveToolSpill(ctx context.Context, runID, toolName, toolCallID, content string) error
	ReadToolSpillForScope(ctx context.Context, runID, toolName, toolCallID, sessionID, userID string) (string, error)
}

type PGArtifactStore struct{ Store ToolSpillStore }

func (s PGArtifactStore) Put(ctx context.Context, scope tool.Scope, name string, content []byte) (model.Artifact, error) {
	if s.Store == nil {
		return model.Artifact{}, fmt.Errorf("artifact store is unavailable")
	}
	callID := strings.TrimSuffix(strings.TrimPrefix(name, "artifact-"), ".json")
	if err := s.Store.SaveToolSpill(ctx, scope.TurnID, "agent-artifact", callID, string(content)); err != nil {
		return model.Artifact{}, err
	}
	return model.Artifact{ID: callID, Name: name, MediaType: "application/json", URI: "spill://" + scope.TurnID + "/agent-artifact/" + callID, SizeBytes: int64(len(content))}, nil
}

type ArtifactReadTool struct{ Store ToolSpillStore }

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
	content, err := t.Store.ReadToolSpillForScope(ctx, parts[0], parts[1], parts[2], call.Scope.SessionID, call.Scope.UserID)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(content), nil
}

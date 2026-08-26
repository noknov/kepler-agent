package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
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

type ArtifactReadTool struct {
	Store          ToolSpillStore
	MaxInlineBytes int
}

func (ArtifactReadTool) Descriptor() tool.Descriptor {
	// Artifact references are emitted by the runtime itself. The reader must be
	// visible in the very next model step, without relying on deferred-tool
	// discovery after a spill has already occurred.
	return tool.Descriptor{Name: "artifact_read", Description: "Read one byte range from a large tool result referenced by a spill:// artifact URI. Use next_offset to request the following range.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"uri":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"required":["uri"]}`), Effects: []tool.Effect{tool.EffectRead}, Exposure: tool.ExposureEager}
}

func (t ArtifactReadTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		URI    string `json:"uri"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
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
	if args.Offset < 0 || args.Offset > len(content) {
		return tool.Result{}, fmt.Errorf("artifact offset is outside the artifact")
	}
	limit := args.Limit
	if limit <= 0 || limit > t.maxPayloadBytes() {
		limit = t.maxPayloadBytes()
	}
	end := min(args.Offset+limit, len(content))
	end = utf8Boundary(content, args.Offset, end)
	fragment := artifactFragment(content[args.Offset:end], args.Offset, end, len(content))
	for encodedResultBytes(fragment) > t.maxInlineBytes() && end > args.Offset {
		end = utf8Boundary(content, args.Offset, args.Offset+(end-args.Offset)/2)
		fragment = artifactFragment(content[args.Offset:end], args.Offset, end, len(content))
	}
	if end == args.Offset && args.Offset < len(content) {
		return tool.Result{}, fmt.Errorf("artifact inline budget is too small to return one UTF-8 character")
	}
	return tool.TextResult(fragment), nil
}

func (t ArtifactReadTool) maxInlineBytes() int {
	if t.MaxInlineBytes <= 0 {
		return 64 << 10
	}
	return t.MaxInlineBytes
}

func (t ArtifactReadTool) maxPayloadBytes() int {
	return t.maxInlineBytes()
}

func artifactFragment(content string, offset, end, total int) string {
	next := ""
	if end < total {
		next = fmt.Sprintf(" next_offset=%d", end)
	}
	return fmt.Sprintf("Artifact bytes %d-%d of %d.%s\n%s", offset, end, total, next, content)
}

func encodedResultBytes(text string) int {
	data, _ := json.Marshal(tool.TextResult(text).Content)
	return len(data)
}

func utf8Boundary(content string, start, end int) int {
	if end >= len(content) {
		return len(content)
	}
	if end <= start {
		return start
	}
	for end > start && !utf8.RuneStart(content[end]) {
		end--
	}
	return end
}

package hosted

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type artifactReadStore struct{ content string }

func (s artifactReadStore) SaveToolSpill(context.Context, string, string, string, string) error {
	return nil
}

func (s artifactReadStore) ReadToolSpillForScope(context.Context, string, string, string, string, string) (string, error) {
	return s.content, nil
}

func TestArtifactReadToolReturnsInlineBoundedFragment(t *testing.T) {
	store := artifactReadStore{content: strings.Repeat("quoted: \\\"value\\\"\n", 200)}
	reader := ArtifactReadTool{Store: store, MaxInlineBytes: 512}
	result, err := reader.Execute(context.Background(), tool.Call{Arguments: []byte(`{"uri":"spill://run/agent-artifact/call","limit":4096}`)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > reader.MaxInlineBytes {
		t.Fatalf("inline result = %d bytes, limit = %d", len(encoded), reader.MaxInlineBytes)
	}
	if !strings.Contains(result.Text(), "next_offset=") {
		t.Fatalf("fragment does not provide a continuation: %q", result.Text())
	}
}

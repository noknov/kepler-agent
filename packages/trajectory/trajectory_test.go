package trajectory

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestBuildRedactsContentAndKeepsOperationalFacts(t *testing.T) {
	metadata, _ := json.Marshal(map[string]any{"provider": "openai", "prompt_hash": "hash", "secret": "no"})
	items := Build([]transcript.Event{{Sequence: 1, Type: transcript.ToolCallCompleted, Timestamp: time.Now(), Metadata: metadata, ToolCall: &tool.Call{ID: "call", Name: "read", Arguments: json.RawMessage(`{"token":"secret"}`)}}})
	if len(items) != 1 || items[0].Summary["provider"] != "openai" || items[0].Summary["secret"] != nil || items[0].Summary["args_bytes"] == nil {
		t.Fatalf("items=%+v", items)
	}
}

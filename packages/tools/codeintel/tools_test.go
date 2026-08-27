package codeintel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
)

func TestSymbolsRequiresRepositoryBeforeStartingLSP(t *testing.T) {
	_, err := (SymbolsTool{}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"Handler"}`)})
	if err == nil || !strings.Contains(err.Error(), "repo is required") {
		t.Fatalf("error = %v, want repo is required", err)
	}
}


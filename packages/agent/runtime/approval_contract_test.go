package runtime

import (
	"encoding/json"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"testing"
)

func TestApprovalContractUsesSemanticSchemaEquality(t *testing.T) {
	original := tool.Descriptor{Name: "write", InputSchema: json.RawMessage(`{"type":"object","properties":{"amount":{"type":"number","minimum":1e3,"maximum":9007199254740993}}}`)}
	equivalent := original
	equivalent.InputSchema = json.RawMessage(`{"properties":{"amount":{"maximum":9007199254740993,"minimum":1000.0,"type":"number"}},"type":"object"}`)
	if !sameApprovalContract(original, equivalent) {
		t.Fatal("JSONB representation changed approval identity")
	}
	changed := equivalent
	changed.InputSchema = json.RawMessage(`{"properties":{"amount":{"maximum":9007199254740992,"minimum":1000,"type":"number"}},"type":"object"}`)
	if sameApprovalContract(original, changed) {
		t.Fatal("numeric precision lost a contract change")
	}
}

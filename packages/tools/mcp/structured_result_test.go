package mcptools

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/mcp"
)

func TestMCPStructuredResultPreservesCanonicalContentTypes(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			raw := json.RawMessage(fmt.Sprintf(`{"structuredContent":{"id":9007199254740993,"label":"中文"},"content":[{"type":"text","text":"summary"},{"type":"image","mimeType":"image/png","data":"aGVsbG8="}],"isError":%t}`, failed))
			value, err := mcp.FormatToolResult(raw)
			if err != nil {
				t.Fatal(err)
			}
			result := Result(value)
			if result.IsError != failed || (result.ErrorCode == "mcp_tool_error") != failed || len(result.Content) != 3 {
				t.Fatalf("MCP outcome or content lost: %+v", result)
			}
			if result.Content[0].Type != model.ContentJSON || string(result.Content[0].JSON) != `{"id":9007199254740993,"label":"中文"}` {
				t.Fatalf("structured content flattened or rounded: %+v", result.Content)
			}
			if result.Content[1].Type != model.ContentText || result.Content[1].Text != "summary" || result.Content[2].Type != model.ContentImage || result.Content[2].ImageURL != "data:image/png;base64,aGVsbG8=" {
				t.Fatalf("unstructured content lost: %+v", result.Content)
			}
		})
	}
}

func TestMCPStructuredResultRequiresAnObject(t *testing.T) {
	for _, value := range []string{`null`, `[]`, `1`, `"text"`} {
		if _, err := mcp.FormatToolResult(json.RawMessage(`{"structuredContent":` + value + `}`)); err == nil {
			t.Fatalf("non-object structuredContent accepted: %s", value)
		}
	}
	for _, value := range []string{`{}`, `{"content":[]}`, `{"structuredContent":{}}`} {
		if _, err := mcp.FormatToolResult(json.RawMessage(value)); err != nil {
			t.Fatalf("valid empty result rejected: %s: %v", value, err)
		}
	}
}

package transcript

import (
	"encoding/json"
	"testing"
)

func TestDecodeEventPreservesNumbersAndRejectsTrailingData(t *testing.T) {
	raw := `{"sequence":42,"tool_result":{"metadata":{"id":9007199254740993}}}`
	event, err := DecodeEvent([]byte(raw + "\n\t"))
	if err != nil || event.Sequence != 42 || event.ToolResult.Metadata["id"] != json.Number("9007199254740993") {
		t.Fatalf("canonical numeric data changed: %+v err=%v", event, err)
	}
	for _, suffix := range []string{`{}`, `null`, ` trailing`} {
		if _, err := DecodeEvent([]byte(raw + suffix)); err == nil {
			t.Fatalf("accepted trailing data: %q", suffix)
		}
	}
}

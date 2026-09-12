package hosted

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestMarshalPostgresJSONPreservesLiteralEscapeAndReplacesNUL(t *testing.T) {
	message := model.TextMessage(model.RoleUser, "actual\x00nul and literal \\u0000")
	payload, err := marshalPostgresJSON(transcript.Event{ID: "e", Message: &message})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(payload) {
		t.Fatalf("invalid JSON: %q", payload)
	}
	var event transcript.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	text := event.Message.Text()
	if strings.ContainsRune(text, '\x00') || !strings.Contains(text, `\u0000`) || !strings.Contains(text, "�") {
		t.Fatalf("text = %q", text)
	}
}

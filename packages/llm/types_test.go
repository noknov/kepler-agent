package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageMarshalContentParts(t *testing.T) {
	msg := Message{
		Role: "user",
		ContentParts: []ContentPart{
			TextPart("look"),
			ImageURLPart("data:image/png;base64,aGVsbG8="),
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"content":[`, `"type":"text"`, `"type":"image_url"`, `"url":"data:image/png;base64,aGVsbG8="`} {
		if !strings.Contains(text, want) {
			t.Fatalf("Message JSON = %s, want %s", text, want)
		}
	}
}

func TestMessageUnmarshalStringContent(t *testing.T) {
	var msg Message
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"hello"}`), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Content != "hello" {
		t.Fatalf("Content = %q, want hello", msg.Content)
	}
}

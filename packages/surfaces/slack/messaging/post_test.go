package messaging

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	slackclient "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

func TestPostAsConnectedUserSplitsSectionText(t *testing.T) {
	var payloads []map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, payload)
		ts := "root"
		if len(payloads) > 1 {
			ts = "continuation"
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"ts":"` + ts + `"}`)), Request: r}, nil
	})

	_, err := PostAsConnectedUser(context.Background(), slackclient.NewTestClient(transport), "C1", "", strings.Repeat("x", slackclient.MaxSectionTextRunes+1), "call-1", Attribution{Name: "斗包"})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 2 {
		t.Fatalf("payloads=%d, want 2", len(payloads))
	}
	for index, payload := range payloads {
		text, _ := payload["text"].(string)
		if utf8.RuneCountInString(text) > slackclient.MaxSectionTextRunes {
			t.Fatalf("part %d has %d runes", index, utf8.RuneCountInString(text))
		}
		blocks, _ := payload["blocks"].([]any)
		if len(blocks) != 2 {
			t.Fatalf("part %d blocks=%#v", index, payload["blocks"])
		}
		section := blocks[0].(map[string]any)
		sectionText := section["text"].(map[string]any)["text"].(string)
		if utf8.RuneCountInString(sectionText) > slackclient.MaxSectionTextRunes {
			t.Fatalf("part %d section has %d runes", index, utf8.RuneCountInString(sectionText))
		}
	}
	if payloads[0]["client_msg_id"] == "" || payloads[0]["client_msg_id"] == payloads[1]["client_msg_id"] {
		t.Fatalf("part delivery IDs=%#v and %#v, want distinct stable IDs", payloads[0]["client_msg_id"], payloads[1]["client_msg_id"])
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

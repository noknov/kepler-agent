package slack

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWorkObjectMetadataRetainsCanonicalIdentityWithoutEditing(t *testing.T) {
	calls := 0
	client := NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var in struct {
			Channel  string `json:"channel"`
			TS       string `json:"ts"`
			Metadata struct {
				Entities []WorkObject `json:"entities"`
			} `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path != "/api/chat.unfurl" || in.Channel != "C1" || in.TS != "123.0001" || len(in.Metadata.Entities) != 1 || in.Metadata.Entities[0].ExternalRef.ID != "task-1" {
			t.Fatal(in, r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	}))
	object := WorkObject{AppUnfurlURL: "https://example.test/tasks/1?tracking=1", URL: "https://example.test/tasks/1", ExternalRef: WorkObjectRef{ID: "task-1"}, EntityType: "slack#/entities/task", EntityPayload: WorkObjectPayload{Attributes: WorkObjectAttributes{Title: WorkObjectTitle{Text: "Task"}}}}
	if err := client.UnfurlWorkObject(t.Context(), "C1", "123.0001", object); err != nil {
		t.Fatal(err)
	}
	object.URL += "?tracking=1"
	if err := client.UnfurlWorkObject(t.Context(), "C1", "123.0001", object); err == nil || calls != 1 {
		t.Fatal("noncanonical entity URL reached Slack")
	}
}

func TestMessagePermalinkResolvesOnlyRequestedEvidence(t *testing.T) {
	calls := 0
	client := NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/api/chat.getPermalink" || r.URL.Query().Get("channel") != "C1" || r.URL.Query().Get("message_ts") != "123.0001" {
			t.Fatal(r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"permalink":"https://example.slack.com/archives/C1/p1230001"}`))}, nil
	}))
	link, err := client.MessagePermalink(t.Context(), "C1", "123.0001")
	if err != nil || link != "https://example.slack.com/archives/C1/p1230001" || calls != 1 {
		t.Fatalf("link=%q err=%v calls=%d", link, err, calls)
	}
}

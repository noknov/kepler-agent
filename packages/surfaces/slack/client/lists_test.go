package slack

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestListReadPreservesSchemaCursorAndNativeValues(t *testing.T) {
	calls := 0
	client := NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var in map[string]any
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path != "/api/slackLists.items.list" || in["list_id"] != "F-list" || in["include_list"] != true || in["limit"] != float64(2) || in["cursor"] != "cursor" {
			t.Fatal(in, r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"list":{"list_metadata":{"schema":[{"id":"Col-title","type":"rich_text"}]}},"items":[{"id":"Rec-1","fields":[{"number":[9007199254740993]}]}],"response_metadata":{"next_cursor":"next"}}`))}, nil
	}))
	page, err := client.ListItems(t.Context(), ListItemsInput{ListID: "F-list", Limit: 2, Cursor: "cursor"})
	if err != nil || calls != 1 || page.NextCursor != "next" || !strings.Contains(string(page.List), "Col-title") || len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), "9007199254740993") {
		t.Fatalf("page=%+v calls=%d err=%v", page, calls, err)
	}
}

func TestListCreatePreservesColumnIdentityAndReceipt(t *testing.T) {
	var in CreateListItemInput
	client := NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/slackLists.items.create" {
			t.Fatal(r.URL)
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"item":{"id":"Rec-created","list_id":"F-list","fields":[{"number":[9007199254740993]}]}}`))}, nil
	}))
	fields := []ListField{{"column_id": json.RawMessage(`"Col-source"`), "message": json.RawMessage(`["https://example.slack.com/archives/C1/p123000000"]`)}}
	item, err := client.CreateListItem(t.Context(), CreateListItemInput{ListID: "F-list", InitialFields: fields, ParentItemID: "Rec-parent"})
	if err != nil || in.ParentItemID != "Rec-parent" || string(in.InitialFields[0]["column_id"]) != `"Col-source"` || !strings.Contains(string(item), "9007199254740993") {
		t.Fatalf("input=%+v receipt=%s err=%v", in, item, err)
	}
}

func TestListCreationNeverReplaysAmbiguousWrites(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		transport  bool
	}{
		{"lost reply", "", 0, true}, {"server failure", "", 503, false}, {"invalid JSON", "{", 200, false},
		{"API partial failure", `{"ok":false,"error":"internal_error"}`, 200, false},
		{"missing identity", `{"ok":true,"item":{}}`, 200, false},
		{"wrong List", `{"ok":true,"item":{"id":"Rec-1","list_id":"other"}}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.transport {
					return nil, io.ErrUnexpectedEOF
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			}))
			_, err := client.CreateListItem(context.Background(), CreateListItemInput{ListID: "F-list", InitialFields: []ListField{{"column_id": json.RawMessage(`"Col1"`), "checkbox": json.RawMessage(`true`)}}})
			var uncertain interface{ DeliveryUncertain() bool }
			if calls != 1 || !errors.As(err, &uncertain) || !uncertain.DeliveryUncertain() {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestListInputsFailBeforeNetwork(t *testing.T) {
	client := NewTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("invalid input reached network"); return nil, nil }))
	for _, fields := range [][]ListField{
		nil, {{"column_id": json.RawMessage(`"Col1"`)}}, {{"column_id": json.RawMessage(`"Col1"`), "text": json.RawMessage(`"wrong native type"`)}},
		{{"column_id": json.RawMessage(`"Col1"`), "checkbox": json.RawMessage(`true`), "user": json.RawMessage(`["U1"]`)}},
		{{"column_id": json.RawMessage(`"Col1"`), "checkbox": json.RawMessage(`true`)}, {"column_id": json.RawMessage(`"Col1"`), "checkbox": json.RawMessage(`false`)}},
	} {
		if _, err := client.CreateListItem(t.Context(), CreateListItemInput{ListID: "F-list", InitialFields: fields}); err == nil {
			t.Fatal("invalid native fields accepted")
		}
	}
	if _, err := client.ListItems(t.Context(), ListItemsInput{ListID: "F-list", Limit: 101}); err == nil {
		t.Fatal("unbounded page accepted")
	}
}

func TestListReadRejectsNonProgressingCursor(t *testing.T) {
	client := NewTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"items":[],"response_metadata":{"next_cursor":"same"}}`))}, nil
	}))
	if _, err := client.ListItems(t.Context(), ListItemsInput{ListID: "F-list", Cursor: "same"}); err == nil {
		t.Fatal("non-progressing page accepted")
	}
}

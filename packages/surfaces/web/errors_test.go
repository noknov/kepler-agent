package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConversationErrorsUseTypesInsteadOfMessageKeywords(t *testing.T) {
	for _, sample := range []struct {
		err    error
		status int
	}{
		{conversationError{errInvalidRequest, "message is required"}, http.StatusBadRequest},
		{fmt.Errorf("wrapped: %w", conversationError{errConversationBusy, "conversation already has an active turn"}), http.StatusConflict},
		{conversationError{errServiceUnavailable, "service is draining; retry shortly"}, http.StatusServiceUnavailable},
		{errors.New("database credentials required; invalid connection"), http.StatusInternalServerError},
		{errors.New("upstream active turn lookup failed"), http.StatusInternalServerError},
	} {
		response := httptest.NewRecorder()
		(&Handler{}).writeConversationError(response, sample.err)
		if response.Code != sample.status {
			t.Fatalf("%v: status=%d, want %d", sample.err, response.Code, sample.status)
		}
		if sample.status == http.StatusInternalServerError && strings.Contains(response.Body.String(), sample.err.Error()) {
			t.Fatalf("internal error exposed: %s", response.Body)
		}
	}
}

func TestDecodeJSONChecksFullBodyAndMediaType(t *testing.T) {
	for _, sample := range []struct {
		name, body, mediaType string
		limit                 int64
		valid                 bool
	}{
		{"exact limit", `{"x":1}`, "application/json", 7, true},
		{"parameters", `{"x":1}`, "application/json; charset=utf-8", 7, true},
		{"lookalike media type", `{"x":1}`, "application/jsonish", 7, false},
		{"oversized whitespace", `{"x":1}` + strings.Repeat(" ", 8), "application/json", 7, false},
		{"second object beyond limit", `{"x":1}` + strings.Repeat(" ", 8) + `{"x":2}`, "application/json", 7, false},
		{"second object", `{"x":1}{"x":2}`, "application/json", 64, false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(sample.body))
			request.Header.Set("Content-Type", sample.mediaType)
			var payload struct {
				X int `json:"x"`
			}
			if err := decodeJSON(request, &payload, sample.limit); (err == nil) != sample.valid {
				t.Fatalf("error=%v valid=%v", err, sample.valid)
			}
		})
	}
}

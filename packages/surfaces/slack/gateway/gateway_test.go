package slackgateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManageInteractionConsumesTriggerSynchronously(t *testing.T) {
	const secret = "signing-secret"
	payload := `{"type":"block_actions","trigger_id":"trigger-1","user":{"id":"U1"},"actions":[{"action_id":"manage_skills","value":"skill"}]}`
	body := url.Values{"payload": []string{payload}}.Encode()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	request := httptest.NewRequest(http.MethodPost, "/slack/interactions", strings.NewReader(body))
	request.Header.Set("X-Slack-Request-Timestamp", timestamp)
	request.Header.Set("X-Slack-Signature", slackTestSignature(secret, timestamp, body))
	recorder := httptest.NewRecorder()

	var called atomic.Bool
	gateway := Gateway{
		SigningSecret: secret,
		OnInteraction: func(_ context.Context, interaction Interaction) *InteractionResponse {
			if interaction.TriggerID != "trigger-1" {
				t.Errorf("TriggerID = %q", interaction.TriggerID)
			}
			called.Store(true)
			return nil
		},
	}
	gateway.HandleInteractions(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !called.Load() {
		t.Fatal("manage interaction returned before trigger was consumed")
	}
}

func TestConnectionChooserSubmissionConsumesTriggerSynchronously(t *testing.T) {
	const secret = "signing-secret"
	payload := `{"type":"view_submission","trigger_id":"trigger-connection-1","user":{"id":"U1"},"view":{"callback_id":"connection_choose","state":{"values":{"connection_provider":{"connection_provider":{"type":"static_select","selected_option":{"value":"clickstack"}}}}}}}`
	body := url.Values{"payload": []string{payload}}.Encode()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	request := httptest.NewRequest(http.MethodPost, "/slack/interactions", strings.NewReader(body))
	request.Header.Set("X-Slack-Request-Timestamp", timestamp)
	request.Header.Set("X-Slack-Signature", slackTestSignature(secret, timestamp, body))
	recorder := httptest.NewRecorder()

	var called atomic.Bool
	gateway := Gateway{
		SigningSecret: secret,
		OnInteraction: func(_ context.Context, interaction Interaction) *InteractionResponse {
			if interaction.TriggerID != "trigger-connection-1" {
				t.Errorf("TriggerID = %q", interaction.TriggerID)
			}
			selected := interaction.View.State["connection_provider"]["connection_provider"].SelectedValues
			if len(selected) != 1 || selected[0] != "clickstack" {
				t.Errorf("selected provider = %#v, want [clickstack]", selected)
			}
			called.Store(true)
			return &InteractionResponse{
				ResponseAction: "update",
				View: map[string]any{
					"type":             "modal",
					"callback_id":      "connection_add",
					"private_metadata": "clickstack",
					"blocks": []map[string]any{{
						"type":     "input",
						"block_id": "connection_mcp_url",
						"element":  map[string]any{"action_id": "mcp_url"},
					}},
				},
			}
		},
	}
	gateway.HandleInteractions(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !called.Load() {
		t.Fatal("connection chooser submission returned before trigger was consumed")
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	rawResponse := recorder.Body.String()
	var response map[string]any
	if err := json.Unmarshal([]byte(rawResponse), &response); err != nil {
		t.Fatalf("decode interaction response: %v", err)
	}
	if response["response_action"] != "update" {
		t.Fatalf("response_action = %#v, want update", response["response_action"])
	}
	view, ok := response["view"].(map[string]any)
	if !ok {
		t.Fatalf("response view = %#v, want modal view", response["view"])
	}
	if view["private_metadata"] != "clickstack" || view["callback_id"] != "connection_add" {
		t.Fatalf("response view metadata = %#v", view)
	}
	if !strings.Contains(rawResponse, "connection_mcp_url") {
		t.Fatalf("response view omitted provider fields: %s", rawResponse)
	}
}

func TestConnectionDetailsSubmissionWritesSynchronousResponse(t *testing.T) {
	const secret = "signing-secret"
	payload := `{"type":"view_submission","trigger_id":"trigger-connection-2","user":{"id":"U1"},"view":{"callback_id":"connection_add","private_metadata":"{\"provider\":\"clickstack\",\"origin\":\"app_home\"}","state":{"values":{"connection_label":{"connection_label":{"type":"plain_text_input","value":"Production"}}}}}}`
	body := url.Values{"payload": []string{payload}}.Encode()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	request := httptest.NewRequest(http.MethodPost, "/slack/interactions", strings.NewReader(body))
	request.Header.Set("X-Slack-Request-Timestamp", timestamp)
	request.Header.Set("X-Slack-Signature", slackTestSignature(secret, timestamp, body))
	recorder := httptest.NewRecorder()

	gateway := Gateway{
		SigningSecret: secret,
		OnInteraction: func(_ context.Context, interaction Interaction) *InteractionResponse {
			if interaction.View.CallbackID != "connection_add" || interaction.View.State["connection_label"]["connection_label"].Value != "Production" {
				t.Errorf("details interaction = %#v", interaction)
			}
			return &InteractionResponse{ResponseAction: "update", View: map[string]any{"type": "modal", "callback_id": "connection_auth"}}
		},
	}
	gateway.HandleInteractions(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response = status %d content-type %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["response_action"] != "update" {
		t.Fatalf("response_action = %#v, want update", response["response_action"])
	}
}

func TestOtherViewSubmissionDispatchesAsynchronously(t *testing.T) {
	const secret = "signing-secret"
	payload := `{"type":"view_submission","trigger_id":"trigger-assets-1","user":{"id":"U1"},"view":{"callback_id":"user_rules_manage","state":{"values":{}}}}`
	body := url.Values{"payload": []string{payload}}.Encode()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	request := httptest.NewRequest(http.MethodPost, "/slack/interactions", strings.NewReader(body))
	request.Header.Set("X-Slack-Request-Timestamp", timestamp)
	request.Header.Set("X-Slack-Signature", slackTestSignature(secret, timestamp, body))
	recorder := httptest.NewRecorder()

	returned := make(chan struct{})
	dispatched := make(chan struct{})
	gateway := Gateway{
		SigningSecret: secret,
		OnInteraction: func(_ context.Context, interaction Interaction) *InteractionResponse {
			if interaction.View.CallbackID != "user_rules_manage" {
				t.Errorf("CallbackID = %q", interaction.View.CallbackID)
			}
			<-returned
			close(dispatched)
			return nil
		},
	}
	done := make(chan struct{})
	go func() {
		gateway.HandleInteractions(recorder, request)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		close(returned)
		t.Fatal("generic view_submission did not return before handler dispatch")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	close(returned)
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("generic view_submission was not dispatched")
	}
}

func slackTestSignature(secret, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":" + body))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

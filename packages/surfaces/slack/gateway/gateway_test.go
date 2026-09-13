package slackgateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
		OnInteraction: func(_ context.Context, interaction Interaction) {
			if interaction.TriggerID != "trigger-1" {
				t.Errorf("TriggerID = %q", interaction.TriggerID)
			}
			called.Store(true)
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

func slackTestSignature(secret, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":" + body))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

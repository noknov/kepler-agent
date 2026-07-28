package slackgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/slack"
)

type Inbox interface {
	Claim(context.Context, string, any) (bool, error)
}

type ErrorWriter func(http.ResponseWriter, *http.Request, int, string, error)

type Gateway struct {
	SigningSecret string
	Inbox         Inbox
	IsDraining    func() bool
	Enqueue       func(context.Context, string, slack.Event) bool
	Publish       func(context.Context, string)
	OnWebSearch   func(string)
	WriteError    ErrorWriter
}

func (g Gateway) HandleEvents(w http.ResponseWriter, r *http.Request) {
	if g.draining() {
		g.error(w, r, http.StatusServiceUnavailable, "server is draining", nil)
		return
	}
	if r.Method != http.MethodPost {
		g.error(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		g.error(w, r, http.StatusBadRequest, "bad request", err)
		return
	}

	body := string(bodyBytes)
	if err := slack.VerifySignature(
		g.SigningSecret,
		r.Header.Get("X-Slack-Request-Timestamp"),
		body,
		r.Header.Get("X-Slack-Signature"),
		time.Now(),
	); err != nil {
		g.error(w, r, http.StatusUnauthorized, "invalid signature", err)
		return
	}

	var envelope slack.EventEnvelope
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		g.error(w, r, http.StatusBadRequest, "bad json", err)
		return
	}
	if envelope.Type == "url_verification" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": envelope.Challenge})
		return
	}
	if envelope.Type != "event_callback" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
	if g.Inbox == nil {
		g.error(w, r, http.StatusServiceUnavailable, "event inbox unavailable", nil)
		return
	}
	claimed, err := g.Inbox.Claim(r.Context(), envelope.EventID, envelope.Event)
	if err != nil {
		g.error(w, r, http.StatusServiceUnavailable, "failed to persist event", err)
		return
	}
	if !claimed {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
	if g.Enqueue != nil {
		if !g.Enqueue(r.Context(), envelope.EventID, envelope.Event) {
			g.error(w, r, http.StatusServiceUnavailable, "event queue is full; please retry", nil)
			return
		}
	} else if g.Publish != nil {
		g.Publish(r.Context(), envelope.EventID)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (g Gateway) HandleInteractions(w http.ResponseWriter, r *http.Request) {
	if g.draining() {
		g.error(w, r, http.StatusServiceUnavailable, "server is draining", nil)
		return
	}
	if r.Method != http.MethodPost {
		g.error(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		g.error(w, r, http.StatusBadRequest, "bad request", err)
		return
	}
	rawBody := string(bodyBytes)
	if err := slack.VerifySignature(
		g.SigningSecret,
		r.Header.Get("X-Slack-Request-Timestamp"),
		rawBody,
		r.Header.Get("X-Slack-Signature"),
		time.Now(),
	); err != nil {
		g.error(w, r, http.StatusUnauthorized, "invalid signature", err)
		return
	}
	body := extractFormPayload(rawBody)
	if body == "" {
		g.error(w, r, http.StatusBadRequest, "missing payload", nil)
		return
	}

	var payload struct {
		Type string `json:"type"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Actions []struct {
			ActionID       string `json:"action_id"`
			SelectedOption struct {
				Value string `json:"value"`
			} `json:"selected_option"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		g.error(w, r, http.StatusBadRequest, "bad json", err)
		return
	}

	w.WriteHeader(http.StatusOK)
	if payload.Type != "block_actions" || g.OnWebSearch == nil {
		return
	}
	for _, action := range payload.Actions {
		if action.ActionID == "toggle_web_search" {
			g.OnWebSearch(payload.User.ID)
		}
	}
}

func (g Gateway) draining() bool {
	return g.IsDraining != nil && g.IsDraining()
}

func (g Gateway) error(w http.ResponseWriter, r *http.Request, status int, message string, err error) {
	if g.WriteError != nil {
		g.WriteError(w, r, status, message, err)
		return
	}
	http.Error(w, message, status)
}

func extractFormPayload(body string) string {
	values, err := url.ParseQuery(body)
	if err != nil {
		return ""
	}
	return values.Get("payload")
}

package slackgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
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
	OnInteraction func(context.Context, Interaction)
	WriteError    ErrorWriter
}

type Interaction struct {
	Type      string
	UserID    string
	TriggerID string
	Channel   string
	ThreadTS  string
	Actions   []InteractionAction
	View      InteractionView
}

type InteractionAction struct {
	ActionID string
	Value    string
}

type InteractionView struct {
	ID         string
	CallbackID string
	State      map[string]map[string]InteractionValue
}

type InteractionValue struct {
	Type           string
	Value          string
	SelectedFiles  []slack.File
	SelectedValues []string
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
		Type      string `json:"type"`
		TriggerID string `json:"trigger_id,omitempty"`
		User      struct {
			ID string `json:"id"`
		} `json:"user"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
		Message struct {
			TS       string `json:"ts"`
			ThreadTS string `json:"thread_ts"`
		} `json:"message"`
		Actions []struct {
			ActionID       string `json:"action_id"`
			Value          string `json:"value,omitempty"`
			SelectedOption struct {
				Value string `json:"value"`
			} `json:"selected_option"`
		} `json:"actions"`
		View struct {
			ID         string `json:"id,omitempty"`
			CallbackID string `json:"callback_id,omitempty"`
			State      struct {
				Values map[string]map[string]struct {
					Type            string       `json:"type,omitempty"`
					Value           string       `json:"value,omitempty"`
					SelectedFiles   []slack.File `json:"files,omitempty"`
					SelectedOptions []struct {
						Value string `json:"value"`
					} `json:"selected_options,omitempty"`
				} `json:"values,omitempty"`
			} `json:"state,omitempty"`
		} `json:"view,omitempty"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		g.error(w, r, http.StatusBadRequest, "bad json", err)
		return
	}

	w.WriteHeader(http.StatusOK)
	if g.OnInteraction == nil {
		return
	}
	interaction := Interaction{
		Type:      payload.Type,
		UserID:    payload.User.ID,
		TriggerID: payload.TriggerID,
		Channel:   payload.Channel.ID,
		ThreadTS:  payload.Message.ThreadTS,
		View: InteractionView{
			ID:         payload.View.ID,
			CallbackID: payload.View.CallbackID,
			State:      map[string]map[string]InteractionValue{},
		},
	}
	if interaction.ThreadTS == "" {
		interaction.ThreadTS = payload.Message.TS
	}
	for _, action := range payload.Actions {
		value := action.Value
		if value == "" {
			value = action.SelectedOption.Value
		}
		interaction.Actions = append(interaction.Actions, InteractionAction{ActionID: action.ActionID, Value: value})
	}
	for blockID, actions := range payload.View.State.Values {
		interaction.View.State[blockID] = map[string]InteractionValue{}
		for actionID, value := range actions {
			var selected []string
			for _, option := range value.SelectedOptions {
				if option.Value != "" {
					selected = append(selected, option.Value)
				}
			}
			interaction.View.State[blockID][actionID] = InteractionValue{
				Type:           value.Type,
				Value:          value.Value,
				SelectedFiles:  value.SelectedFiles,
				SelectedValues: selected,
			}
		}
	}
	go g.OnInteraction(context.Background(), interaction)
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

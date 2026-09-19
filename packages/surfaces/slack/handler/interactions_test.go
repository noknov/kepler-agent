package slackhandler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	slackclient "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	slackgateway "github.com/noknov/kepler-agent/packages/surfaces/slack/gateway"
	slackhome "github.com/noknov/kepler-agent/packages/surfaces/slack/home"
	"github.com/noknov/kepler-agent/packages/userprefs"
)

type dedupeInputStore struct{ items []sessioninput.Item }

func (s *dedupeInputStore) Enqueue(_ context.Context, item sessioninput.Item) error {
	for _, existing := range s.items {
		if existing.ID == item.ID {
			return nil
		}
	}
	s.items = append(s.items, item)
	return nil
}
func (*dedupeInputStore) Claim(context.Context, string, sessioninput.Kind, string, time.Duration, int) ([]sessioninput.Item, error) {
	return nil, nil
}
func (*dedupeInputStore) Ack(context.Context, string, string) error     { return nil }
func (*dedupeInputStore) Release(context.Context, string, string) error { return nil }
func (*dedupeInputStore) PendingSessions(context.Context, sessioninput.Kind, int) ([]string, error) {
	return nil, nil
}
func (*dedupeInputStore) PromoteSteering(context.Context, string) (int64, error) { return 0, nil }
func (*dedupeInputStore) PromoteExpiredSteering(context.Context, time.Duration) (int64, error) {
	return 0, nil
}

type failingInputStore struct{ *dedupeInputStore }

func (*failingInputStore) Enqueue(context.Context, sessioninput.Item) error {
	return errors.New("queue unavailable")
}

type slackHandlerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f slackHandlerRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestParseAssetActionValue(t *testing.T) {
	kind, id, ok := parseAssetActionValue("rule:U1:rule:style")
	if !ok || kind != "rule" || id != "U1:rule:style" {
		t.Fatalf("parseAssetActionValue() = %q, %q, %v", kind, id, ok)
	}
}

func TestConnectionModalIsProgressiveAndProviderDeclared(t *testing.T) {
	choose := connectionAddModal([]connections.Plugin{{ID: connections.ProviderClickStack, Title: "ClickStack"}})
	raw, err := json.Marshal(choose)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), connectionChooseCallbackID) || strings.Contains(string(raw), "Server URL") {
		t.Fatalf("provider chooser should not show provider fields: %s", raw)
	}
	details := connectionDetailsModal(connections.Plugin{
		ID: connections.ProviderClickStack, Title: "ClickStack", SupportsMultiple: true,
		Fields: []connections.PluginField{{ID: "mcp_url", Label: "Server URL", Required: false}},
	})
	raw, err = json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, connectionCallbackID) || !strings.Contains(body, "Server URL") || !strings.Contains(body, "private_metadata") {
		t.Fatalf("provider details missing declared fields: %s", body)
	}
}

func TestAssetModalShowsImmediateActivationAndActiveControls(t *testing.T) {
	modal := assetModal(userprefs.KindSkill, []userprefs.Asset{
		{ID: "U1:skill:active", Name: "active", Active: true},
		{ID: "U1:skill:disabled", Name: "disabled", Active: false},
	})
	raw, err := json.Marshal(modal)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"New uploads are enabled immediately",
		"built-in rules and skills remain available",
		"disable_asset",
		"enable_asset",
		"delete_asset",
		"Active",
		"Disabled",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("asset modal missing %q: %s", want, body)
		}
	}
}

func TestDuplicateSlackApprovalUsesOneStableQueueItem(t *testing.T) {
	inputs := &dedupeInputStore{}
	h := &Handler{Inputs: inputs}
	interaction := slackgateway.Interaction{
		UserID: "U1", Channel: "D1", ThreadTS: "123.456",
	}
	payload := `{"turn_id":"turn-1","tool_call_id":"call-1"}`
	h.resolveApproval(context.Background(), interaction, "agent_approval_approve", payload)
	h.resolveApproval(context.Background(), interaction, "agent_approval_approve", payload)
	if len(inputs.items) != 1 {
		t.Fatalf("duplicate approval enqueued %d items, want 1", len(inputs.items))
	}
	if inputs.items[0].ID != "approval-turn-1-call-1-approve" {
		t.Fatalf("queue item id = %q", inputs.items[0].ID)
	}
}

func TestApprovalEnqueueFailureRestoresRetryableButtons(t *testing.T) {
	var updates []map[string]any
	slack := slackclient.NewTestClient(slackHandlerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		updates = append(updates, payload)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	}))
	h := &Handler{
		Inputs: &failingInputStore{dedupeInputStore: &dedupeInputStore{}},
		Slack:  slack,
	}
	interaction := slackgateway.Interaction{UserID: "U1", Channel: "D1", ThreadTS: "123.456", MessageTS: "123.457"}
	payload := `{"turn_id":"turn-2","tool_call_id":"call-2"}`
	h.resolveApproval(context.Background(), interaction, "agent_approval_approve", payload)
	if len(updates) != 2 {
		t.Fatalf("chat.update calls = %d, want pending and retryable failure", len(updates))
	}
	if got := updates[1]["text"]; got != "Approval failed — retry available" {
		t.Fatalf("failure message text = %q", got)
	}
	blocks, ok := updates[1]["blocks"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("failure blocks = %#v, want context plus actions", updates[1]["blocks"])
	}
	actions, ok := blocks[1].(map[string]any)["elements"].([]any)
	if !ok || len(actions) != 2 {
		t.Fatalf("failure actions = %#v, want two retry buttons", blocks[1])
	}
	for _, raw := range actions {
		button := raw.(map[string]any)
		if button["value"] != payload {
			t.Fatalf("retry button value = %q, want stable approval payload", button["value"])
		}
	}
}

func TestProviderAddActionOpensDeclaredDetailsModal(t *testing.T) {
	var opened map[string]any
	slack := slackclient.NewTestClient(slackHandlerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		opened = payload
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	}))
	h := &Handler{
		Slack: slack,
		Home:  slackhome.Controller{Connections: connections.Service{Config: connections.Config{PublicBaseURL: "https://example.test", SecretKey: "secret"}}},
	}
	h.handleBlockActions(context.Background(), slackgateway.Interaction{
		UserID:    "U1",
		TriggerID: "trigger-1",
		Actions:   []slackgateway.InteractionAction{{ActionID: "add_connection", Value: connections.ProviderClickStack}},
	})
	view, ok := opened["view"].(map[string]any)
	if !ok || view["callback_id"] != connectionCallbackID {
		t.Fatalf("opened view = %#v, want provider details modal", opened)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "mcp_url") {
		t.Fatalf("provider details modal omitted declared fields: %s", raw)
	}
}

func TestConnectionChooserSubmissionReturnsDeclaredDetailsModal(t *testing.T) {
	h := &Handler{
		Home: slackhome.Controller{Connections: connections.Service{Config: connections.Config{
			PublicBaseURL: "https://example.test",
			SecretKey:     "secret",
		}}},
	}

	response := h.HandleInteraction(context.Background(), slackgateway.Interaction{
		Type:      "view_submission",
		UserID:    "U1",
		TriggerID: "trigger-1",
		View: slackgateway.InteractionView{
			CallbackID: connectionChooseCallbackID,
			State: map[string]map[string]slackgateway.InteractionValue{
				"connection_provider": {
					"connection_provider": {SelectedValues: []string{connections.ProviderClickStack}},
				},
			},
		},
	})
	if response == nil || response.ResponseAction != "update" {
		t.Fatalf("interaction response = %#v, want update response", response)
	}
	view := response.View
	if view["callback_id"] != connectionCallbackID {
		t.Fatalf("response view metadata = %#v", view)
	}
	modalContext, ok := view["private_metadata"].(string)
	if !ok {
		t.Fatalf("response provider metadata = %#v", view["private_metadata"])
	}
	decodedContext := decodeConnectionModalContext(modalContext)
	if decodedContext.Provider != connections.ProviderClickStack {
		t.Fatalf("response provider metadata = %#v", view["private_metadata"])
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "mcp_url") {
		t.Fatalf("provider details modal omitted declared fields: %s", raw)
	}
}

func TestConnectionChooserSubmissionRejectsInvalidProvider(t *testing.T) {
	h := &Handler{Home: slackhome.Controller{Connections: connections.Service{Config: connections.Config{
		PublicBaseURL: "https://example.test",
		SecretKey:     "secret",
	}}}}
	response := h.HandleInteraction(context.Background(), slackgateway.Interaction{
		Type:   "view_submission",
		UserID: "U1",
		View:   slackgateway.InteractionView{CallbackID: connectionChooseCallbackID, State: map[string]map[string]slackgateway.InteractionValue{"connection_provider": {"connection_provider": {SelectedValues: []string{"not-a-provider"}}}}},
	})
	if response == nil || response.ResponseAction != "errors" {
		t.Fatalf("interaction response = %#v, want errors response", response)
	}
	if message := response.Errors["connection_provider"]; message == "" {
		t.Fatalf("errors response missing provider message: %#v", response.Errors)
	}
}

func TestConnectionDetailsSubmissionReturnsAuthModalWithExplicitOrigin(t *testing.T) {
	tests := []struct {
		name          string
		origin        string
		returnContext string
	}{
		{name: "app home", origin: "app_home"},
		{name: "chat", origin: "chat", returnContext: `{"channel":"C1","thread_ts":"171.1"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := &Handler{Home: slackhome.Controller{Connections: connections.Service{Config: connections.Config{
				PublicBaseURL: "https://example.test",
				SecretKey:     "secret",
			}}}}
			privateMetadata := encodeConnectionModalContext(connectionModalContext{
				Provider:      connections.ProviderClickStack,
				Origin:        test.origin,
				ReturnContext: test.returnContext,
			})
			response := h.HandleInteraction(context.Background(), slackgateway.Interaction{
				Type:   "view_submission",
				UserID: "U1",
				View: slackgateway.InteractionView{
					CallbackID:      connectionCallbackID,
					PrivateMetadata: privateMetadata,
					State: map[string]map[string]slackgateway.InteractionValue{
						"connection_label":   {"connection_label": {Value: "Production"}},
						"connection_mcp_url": {"mcp_url": {Value: "https://clickstack.example/mcp"}},
					},
				},
			})
			if response == nil || response.ResponseAction != "update" {
				t.Fatalf("interaction response = %#v, want update response", response)
			}
			if response.View["callback_id"] != connectionCallbackID {
				t.Fatalf("auth view callback_id = %#v", response.View["callback_id"])
			}
			raw, err := json.Marshal(response.View)
			if err != nil {
				t.Fatal(err)
			}
			body := string(raw)
			for _, want := range []string{"Open authorization page", "origin=" + test.origin, "label=Production"} {
				if !strings.Contains(body, want) {
					t.Fatalf("auth view omitted %q: %s", want, body)
				}
			}
			if test.returnContext != "" && !strings.Contains(body, "return_context=") {
				t.Fatalf("chat auth view omitted return context: %s", body)
			}
		})
	}
}

func TestConnectionDetailsSubmissionRejectsInvalidName(t *testing.T) {
	h := &Handler{Home: slackhome.Controller{Connections: connections.Service{Config: connections.Config{
		PublicBaseURL: "https://example.test",
		SecretKey:     "secret",
	}}}}
	response := h.HandleInteraction(context.Background(), slackgateway.Interaction{
		Type:   "view_submission",
		UserID: "U1",
		View: slackgateway.InteractionView{
			CallbackID: connectionCallbackID,
			PrivateMetadata: encodeConnectionModalContext(connectionModalContext{
				Provider: connections.ProviderClickStack,
				Origin:   "app_home",
			}),
			State: map[string]map[string]slackgateway.InteractionValue{
				"connection_label": {"connection_label": {Value: strings.Repeat("x", 81)}},
			},
		},
	})
	if response == nil || response.ResponseAction != "errors" || response.Errors["connection_label"] == "" {
		t.Fatalf("invalid name response = %#v, want connection_label error", response)
	}
}

package slackhandler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/noknov/kepler-agent/packages/connections"
	slackgateway "github.com/noknov/kepler-agent/packages/surfaces/slack/gateway"
	slackhome "github.com/noknov/kepler-agent/packages/surfaces/slack/home"
)

type connectionHomePublisher struct{ calls int }

func (p *connectionHomePublisher) PublishHome(context.Context, string, map[string]any) error {
	p.calls++
	return nil
}

func TestRemoveConnectionUsesActorAndExactInstance(t *testing.T) {
	ctx := context.Background()
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ user, id string }{{"U1", "default"}, {"U1", "i-eu"}, {"U2", "i-eu"}} {
		if err := store.UpsertTokenInstance(ctx, item.user, connections.ProviderClickStack, item.id, "", "token", nil, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	pub := &connectionHomePublisher{}
	h := Handler{Home: slackhome.Controller{Slack: pub, Connections: connections.Service{Store: store, OnConnectionChanged: func(context.Context, string, string) error {
		t.Fatal("removal must not signal OAuth completion")
		return nil
	}}}}
	act := func(user, value string) {
		h.HandleInteraction(ctx, slackgateway.Interaction{Type: "block_actions", UserID: user, Actions: []slackgateway.InteractionAction{{ActionID: "remove_connection", Value: value}}})
	}
	value, _ := json.Marshal(slackhome.ConnectionReference{Provider: connections.ProviderClickStack, InstanceID: "i-eu"})
	act("U1", string(value))
	if _, err := store.GetInstance(ctx, "U1", connections.ProviderClickStack, "i-eu"); err == nil {
		t.Fatal("selected connection remains")
	}
	for _, item := range []struct{ user, id string }{{"U1", "default"}, {"U2", "i-eu"}} {
		if _, err := store.GetInstance(ctx, item.user, connections.ProviderClickStack, item.id); err != nil {
			t.Fatalf("sibling/user connection deleted: %v", err)
		}
	}
	if pub.calls != 1 {
		t.Fatalf("home not refreshed: %d", pub.calls)
	}
	act("", `{"provider":"clickstack","instance_id":"default"}`)
	act("U1", `{"provider":"clickstack"}`)
	act("U1", `{"provider":"clickstack","instance_id":"Default "}`)
	if _, err := store.GetInstance(ctx, "U1", connections.ProviderClickStack, "default"); err != nil {
		t.Fatal("malformed or unauthenticated removal deleted unnamed connection")
	}
	act("U1", `{"provider":"clickstack","instance_id":"default"}`)
	if _, err := store.GetInstance(ctx, "U1", connections.ProviderClickStack, "default"); err == nil {
		t.Fatal("unnamed connection cannot be removed")
	}
}

func TestNewCloudConnectionRequiresUsersServiceID(t *testing.T) {
	h := Handler{Home: slackhome.Controller{Connections: connections.Service{Config: connections.Config{PublicBaseURL: "https://example.test", SecretKey: "secret", ClickStack: connections.ClickStackOAuthConfig{ServiceID: "operator-service"}}}}}
	interaction := slackgateway.Interaction{Type: "view_submission", UserID: "U1", View: slackgateway.InteractionView{CallbackID: connectionCallbackID, PrivateMetadata: encodeConnectionModalContext(connectionModalContext{Provider: connections.ProviderClickStack, Origin: "app_home"}), State: map[string]map[string]slackgateway.InteractionValue{}}}
	response := h.HandleInteraction(context.Background(), interaction)
	if response == nil || response.Errors["connection_service_id"] == "" {
		t.Fatalf("missing service ID silently used operator target: %+v", response)
	}
	interaction.View.State["connection_service_id"] = map[string]slackgateway.InteractionValue{"service_id": {Value: "eu-service"}}
	response = h.HandleInteraction(context.Background(), interaction)
	if response == nil || response.ResponseAction != "update" {
		t.Fatalf("explicit service ID rejected: %+v", response)
	}
}

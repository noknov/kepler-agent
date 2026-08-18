package hostedtools

import (
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/connections"
)

func TestPolicyForSurfaceEnablesSlackConnectionDeps(t *testing.T) {
	conn := &connections.Service{
		Config: connections.Config{
			Slack: connections.SlackOAuthConfig{
				ClientID:     "id",
				ClientSecret: "secret",
			},
		},
	}
	policy := PolicyForSurface(config.Config{}, SurfaceOptions{
		Name:          "slack",
		AvailableDeps: map[string]bool{"slack": true},
		Connections:   conn,
	})
	if !policy.AvailableDeps["slack-connection"] {
		t.Fatal("expected slack-connection dependency to be available when Slack OAuth is configured")
	}
}

func TestPolicyForSurfaceEnablesClickStackDeps(t *testing.T) {
	policy := PolicyForSurface(config.Config{
		Integrations: config.IntegrationConfig{
			ClickStack: config.ClickStackConfig{ServiceID: "svc-1"},
		},
	}, SurfaceOptions{Name: "slack"})
	if !policy.AvailableDeps["clickstack"] {
		t.Fatal("expected clickstack dependency when CLICKSTACK_SERVICE_ID is configured")
	}
}

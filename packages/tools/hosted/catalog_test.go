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

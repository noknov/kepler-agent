package hostedtools

import (
	"testing"

	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/safety"
)

func TestApprovalToolExposureRequiresConfiguredSlackOperator(t *testing.T) {
	for _, surface := range []string{"slack", "web", "cli"} {
		for _, configured := range []bool{false, true} {
			cfg := config.Config{}
			cfg.Integrations.GitHub.Token = "token"
			if configured {
				cfg.Integrations.GitHub.ApprovalNotifyUserID = "owner"
			}
			bundle, err := NewCatalog(cfg, safety.WorkspacePolicy{}, nil, SurfaceOptions{Name: surface})
			if err != nil {
				t.Fatal(err)
			}
			_, found := bundle.Catalog.Get("github-approve_pr")
			if found != (surface == "slack" && configured) {
				t.Fatalf("surface=%s configured=%v exposed=%v", surface, configured, found)
			}
		}
	}
}

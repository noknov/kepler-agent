package connections

import "testing"

func TestClickStackTargetMetadataDoesNotInheritAnotherTargetsHeaders(t *testing.T) {
	base := ClickStackOAuthConfig{MCPURL: clickstackDefaultMCPURL, ServiceID: "operator-service", TeamID: "operator-team"}
	for _, test := range []struct {
		name               string
		metadata           map[string]string
		service, team, url string
	}{
		{name: "legacy", service: "operator-service", team: "operator-team", url: clickstackDefaultMCPURL},
		{name: "cloud", metadata: map[string]string{"service_id": "eu-service"}, service: "eu-service", url: clickstackDefaultMCPURL},
		{name: "custom server", metadata: map[string]string{"mcp_url": "https://oss.example/api/mcp", "team_id": "eu-team"}, team: "eu-team", url: "https://oss.example/api/mcp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := base.WithMetadata(test.metadata)
			if got.ServiceID != test.service || got.TeamID != test.team || got.MCPURL != test.url {
				t.Fatalf("target=%+v", got)
			}
		})
	}
	for _, value := range []string{"bad\r\nheader", "bad\x00header"} {
		if err := validateConnectionMetadata(ProviderClickStack, map[string]string{"service_id": value}); err == nil {
			t.Fatal("header control accepted")
		}
	}
}

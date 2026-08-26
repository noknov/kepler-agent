package clickstack

import (
	"testing"

	"github.com/noknov/kepler-agent/packages/config"
)

func TestNewMCPClientHeaders(t *testing.T) {
	client := NewMCPClient(config.ClickStackConfig{
		MCPURL:    "https://clickstack.example/api/mcp",
		ServiceID: "svc-1",
		TeamID:    "team-1",
	}, "token")
	if client.URL != "https://clickstack.example/api/mcp" {
		t.Fatalf("URL = %q", client.URL)
	}
	if client.Token != "token" {
		t.Fatalf("Token = %q", client.Token)
	}
	if client.Headers["x-service-id"] != "svc-1" || client.Headers["x-hdx-team"] != "team-1" {
		t.Fatalf("headers = %#v", client.Headers)
	}
}

func TestNewMCPClientDefaultURL(t *testing.T) {
	client := NewMCPClient(config.ClickStackConfig{}, "token")
	if client.URL != defaultMCPURL {
		t.Fatalf("URL = %q", client.URL)
	}
}

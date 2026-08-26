package notion

import (
	"testing"

	"github.com/noknov/kepler-agent/packages/config"
)

func TestNewMCPClientDefaultURL(t *testing.T) {
	client := NewMCPClient(config.NotionConfig{}, "token")
	if client.URL != defaultMCPURL {
		t.Fatalf("URL = %q", client.URL)
	}
	if client.Token != "token" {
		t.Fatalf("Token = %q", client.Token)
	}
}

func TestNewMCPClientCustomURL(t *testing.T) {
	client := NewMCPClient(config.NotionConfig{MCPURL: "https://notion.example/mcp"}, "token")
	if client.URL != "https://notion.example/mcp" {
		t.Fatalf("URL = %q", client.URL)
	}
}

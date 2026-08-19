package connections

import "testing"

func TestNotionOAuthEnabled(t *testing.T) {
	cfg := NotionOAuthConfig{ClientID: "id", ClientSecret: "secret"}
	if !cfg.Enabled() {
		t.Fatal("expected notion oauth to be enabled")
	}
}

func TestConfigNotionFlags(t *testing.T) {
	cfg := Config{
		PublicBaseURL: "https://example.com",
		Notion:        NotionOAuthConfig{ClientID: "id", ClientSecret: "secret"},
	}
	if !cfg.NotionEnabled() {
		t.Fatal("expected notion enabled")
	}
	if !cfg.OAuthEnabled() {
		t.Fatal("expected OAuthEnabled true")
	}
}

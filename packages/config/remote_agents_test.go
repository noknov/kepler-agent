package config

import "testing"

func TestRemoteAgentConfigurationHasExplicitTrustedTargets(t *testing.T) {
	for _, url := range []string{"https://peer.example/a2a", "http://127.0.0.1:9000/a2a", "http://[::1]:9000/a2a"} {
		if err := ValidateRemoteAgents([]RemoteAgentConfig{{Name: "peer", URL: url}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, url := range []string{"http://remote.example/a2a", "file:///etc/passwd", "https://user:pass@peer.example/a2a", "https://peer.example/a2a?token=private", "https://peer.example/a2a#fragment"} {
		if err := ValidateRemoteAgents([]RemoteAgentConfig{{Name: "peer", URL: url}}); err == nil {
			t.Fatalf("invalid endpoint accepted: %s", url)
		}
	}
	if err := ValidateRemoteAgents([]RemoteAgentConfig{{Name: "peer", URL: "https://one.example"}, {Name: "peer", URL: "https://two.example"}}); err == nil {
		t.Fatal("duplicate target name accepted")
	}
}

package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// RemoteAgentConfig is operator configuration, never model-generated routing.
// Authentication stays in the named environment variable, not in this document.
type RemoteAgentConfig struct {
	Name             string `json:"name" toml:"name"`
	URL              string `json:"url" toml:"url"`
	TokenEnv         string `json:"token_env,omitempty" toml:"token_env"`
	TimeoutSeconds   int    `json:"timeout_seconds,omitempty" toml:"timeout_seconds"`
	MaxResponseBytes int    `json:"max_response_bytes,omitempty" toml:"max_response_bytes"`
}

func ValidateRemoteAgents(agents []RemoteAgentConfig) error {
	seen := make(map[string]bool)
	for _, agent := range agents {
		if agent.Name == "" || len(agent.Name) > 40 || seen[agent.Name] {
			return fmt.Errorf("remote agent names must be nonempty, unique and at most 40 bytes")
		}
		seen[agent.Name] = true
		for _, c := range agent.Name {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return fmt.Errorf("remote agent name contains an invalid character")
			}
		}
		endpoint, err := url.Parse(agent.URL)
		if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return fmt.Errorf("remote agent endpoint must be absolute and contain no credentials, query or fragment")
		}
		loopback := endpoint.Hostname() == "localhost"
		if ip := net.ParseIP(endpoint.Hostname()); ip != nil {
			loopback = ip.IsLoopback()
		}
		if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && loopback) {
			return fmt.Errorf("remote agent requires HTTPS or loopback HTTP")
		}
		if agent.TimeoutSeconds < 0 || int64(agent.TimeoutSeconds) > (1<<63-1)/int64(time.Second) || agent.MaxResponseBytes < 0 || strings.ContainsAny(agent.TokenEnv, "=\x00") {
			return fmt.Errorf("invalid remote agent resource or credential configuration")
		}
	}
	return nil
}

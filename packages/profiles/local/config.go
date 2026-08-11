package local

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Provider             string            `toml:"provider"`
	Protocol             string            `toml:"protocol"`
	AnthropicFlavor      string            `toml:"anthropic_flavor"`
	Model                string            `toml:"model"`
	BaseURL              string            `toml:"base_url"`
	APIKeyEnv            string            `toml:"api_key_env"`
	ReasoningEffort      string            `toml:"reasoning_effort"`
	InputRouting         string            `toml:"input_routing"`
	Output               string            `toml:"output"`
	MaxSteps             int               `toml:"max_steps"`
	MaxOutputTokens      int               `toml:"max_output_tokens"`
	Temperature          float64           `toml:"temperature"`
	Timeout              time.Duration     `toml:"timeout"`
	UnsafeAllowNoSandbox bool              `toml:"unsafe_allow_no_sandbox"`
	AdditionalReadRoots  []string          `toml:"additional_read_roots"`
	PromptFiles          []string          `toml:"prompt_files"`
	SkillRoots           []string          `toml:"skill_roots"`
	MCPServers           []MCPServerConfig `toml:"mcp_servers"`
}

type MCPServerConfig struct {
	Name     string   `toml:"name"`
	URL      string   `toml:"url"`
	TokenEnv string   `toml:"token_env"`
	Effects  []string `toml:"effects"`
}

func DefaultConfig() Config {
	return Config{Provider: "openai", Protocol: "openai", InputRouting: "steer", Output: "text", MaxSteps: 32, MaxOutputTokens: 16384, Timeout: 30 * time.Minute}
}

func LoadConfig(path string) (Config, error) {
	config := DefaultConfig()
	if path == "" {
		candidate, err := DefaultConfigPath()
		if err != nil {
			return config, err
		}
		path = candidate
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		return config, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := config.Validate(); err != nil {
		return config, err
	}
	return config, nil
}

func (config Config) Validate() error {
	if strings.TrimSpace(config.Provider) == "" {
		return fmt.Errorf("provider is required")
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		return fmt.Errorf("base_url is required")
	}
	if strings.TrimSpace(config.APIKeyEnv) == "" {
		return fmt.Errorf("api_key_env is required")
	}
	if config.Protocol != "openai" && config.Protocol != "anthropic" && config.Protocol != "responses" {
		return fmt.Errorf("protocol must be openai, responses, or anthropic")
	}
	if config.InputRouting != "steer" && config.InputRouting != "queue" {
		return fmt.Errorf("input_routing must be steer or queue")
	}
	if config.Output != "text" && config.Output != "jsonl" {
		return fmt.Errorf("output must be text or jsonl")
	}
	return nil
}

func DefaultConfigPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "slack-copilot-agent", "config.toml"), nil
}

func DefaultStateDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "slack-copilot-agent"), nil
}

func LoadPromptFiles(paths []string) ([]string, error) {
	values := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read prompt file %s: %w", path, err)
		}
		values = append(values, string(data))
	}
	return values, nil
}

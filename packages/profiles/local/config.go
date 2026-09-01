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
	InputRouting         string            `toml:"input_routing"`
	Output               string            `toml:"output"`
	MaxSteps             int               `toml:"max_steps"`
	MaxOutputTokens      int               `toml:"max_output_tokens"`
	MaxContextTokens     int               `toml:"max_context_tokens"`
	AutocompactBuffer    int               `toml:"autocompact_buffer"`
	Timeout              time.Duration     `toml:"timeout"`
	UnsafeAllowNoSandbox bool              `toml:"unsafe_allow_no_sandbox"`
	AdditionalReadRoots  []string          `toml:"additional_read_roots"`
	PromptFiles          []string          `toml:"prompt_files"`
	SkillRoots           []string          `toml:"skill_roots"`
	MCPServers           []MCPServerConfig `toml:"mcp_servers"`

	Provider        string `toml:"-"`
	Protocol        string `toml:"-"`
	AnthropicFlavor string `toml:"-"`
	Model           string `toml:"-"`
	ReasoningEffort string `toml:"-"`
}

type MCPServerConfig struct {
	Name     string            `toml:"name"`
	URL      string            `toml:"url"`
	TokenEnv string            `toml:"token_env"`
	Effects  []string          `toml:"effects"`
	Headers  map[string]string `toml:"headers"`
}

const configDirectory = "kepler-agent"

func DefaultConfig() Config {
	return Config{InputRouting: "steer", Output: "text", MaxSteps: 256, MaxOutputTokens: 16384, MaxContextTokens: 96_000, AutocompactBuffer: 8_000, Timeout: 30 * time.Minute}
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
	if config.MaxSteps <= 0 {
		config.MaxSteps = DefaultConfig().MaxSteps
	}
	if err := config.Validate(); err != nil {
		return config, err
	}
	return config, nil
}

func (config Config) Validate() error {
	if config.InputRouting != "steer" && config.InputRouting != "queue" {
		return fmt.Errorf("input_routing must be steer or queue")
	}
	if config.Output != "text" && config.Output != "jsonl" {
		return fmt.Errorf("output must be text or jsonl")
	}
	if config.MaxContextTokens <= 0 || config.AutocompactBuffer < 0 || config.AutocompactBuffer >= config.MaxContextTokens {
		return fmt.Errorf("max_context_tokens must be positive and autocompact_buffer must be smaller")
	}
	return nil
}

func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", configDirectory, "config.toml"), nil
}

func DefaultStateDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, configDirectory), nil
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

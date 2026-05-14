package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsCrossProviderEnvMixing(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"ANTHROPIC_BASE_URL":   "https://api.kimi.com/coding/",
		"ANTHROPIC_AUTH_TOKEN": "dotenv-token",
		"ANTHROPIC_MODEL":      "kimi-for-coding",
	})
	t.Setenv("OPENAI_BASE_URL", "https://api.deepseek.com")
	t.Setenv("OPENAI_API_KEY", "shell-key")
	t.Setenv("OPENAI_MODEL", "deepseek-chat")

	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "ALLOW_ENV_MIXING") {
		t.Fatalf("Load() error = %v, want provider mixing error", err)
	}
}

func TestLoadPrefersDotEnvWhenConfigured(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"PREFER_DOTENV":        "true",
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"LLM_PROTOCOL":         "anthropic",
		"ANTHROPIC_BASE_URL":   "https://api.kimi.com/coding/",
		"ANTHROPIC_AUTH_TOKEN": "dotenv-token",
		"ANTHROPIC_MODEL":      "kimi-for-coding",
	})
	t.Setenv("OPENAI_BASE_URL", "https://api.deepseek.com")
	t.Setenv("OPENAI_API_KEY", "shell-key")
	t.Setenv("OPENAI_MODEL", "deepseek-chat")

	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.LLM.Protocol != "anthropic" {
		t.Fatalf("LLM.Protocol = %q, want anthropic", cfg.LLM.Protocol)
	}
	if cfg.LLM.Model != "kimi-for-coding" {
		t.Fatalf("LLM.Model = %q, want kimi-for-coding", cfg.LLM.Model)
	}
}

func TestLoadAllowsAnthropicCodingEndpointWithoutExperimentalOptIn(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"LLM_PROTOCOL":         "anthropic",
		"ANTHROPIC_BASE_URL":   "https://api.kimi.com/coding/",
		"ANTHROPIC_AUTH_TOKEN": "token",
		"ANTHROPIC_MODEL":      "kimi-for-coding",
	})

	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.LLM.Protocol != "anthropic" {
		t.Fatalf("LLM.Protocol = %q, want anthropic", cfg.LLM.Protocol)
	}
}

func TestLoadRejectsOpenAICodingEndpointWithoutExplicitOptIn(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"LLM_PROTOCOL":         "openai",
		"ANTHROPIC_BASE_URL":   "https://api.kimi.com/coding/",
		"ANTHROPIC_AUTH_TOKEN": "token",
		"ANTHROPIC_MODEL":      "kimi-for-coding",
	})

	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "ALLOW_EXPERIMENTAL_CODING_ENDPOINT") {
		t.Fatalf("Load() error = %v, want coding opt-in error", err)
	}
}

func writeEnvFile(t *testing.T, dir string, values map[string]string) {
	t.Helper()
	lines := make([]string, 0, len(values))
	for key, value := range values {
		lines = append(lines, key+"="+value)
	}
	data := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func resetConfigEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"SLACK_BOT_TOKEN",
		"SLACK_SIGNING_SECRET",
		"SLACK_BOT_USER_ID",
		"ALLOWED_SLACK_USERS",
		"ALLOWED_SLACK_CHANNELS",
		"LLM_PROTOCOL",
		"LLM_ANTHROPIC_FLAVOR",
		"KIMI_PROTOCOL",
		"ANTHROPIC_PROTOCOL",
		"ANTHROPIC_FLAVOR",
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OPENAI_MODEL",
		"KIMI_API_KEY",
		"KIMI_BASE_URL",
		"KIMI_MODEL",
		"MOONSHOT_API_KEY",
		"MOONSHOT_BASE_URL",
		"MOONSHOT_MODEL",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_MODEL",
		"ALLOW_ENV_MIXING",
		"PREFER_DOTENV",
		"ALLOW_EXPERIMENTAL_CODING_ENDPOINT",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
}

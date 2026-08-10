package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPrefersDotEnvOverShellEnv(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"LLM_PROVIDER":         "cliproxyapi",
		"CLIPROXYAPI_API_KEY":  "dotenv-token",
		"CLIPROXYAPI_MODEL":    "kimi/kimi-k2.7-code",
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
	if cfg.LLM.Protocol != "openai" {
		t.Fatalf("LLM.Protocol = %q, want openai", cfg.LLM.Protocol)
	}
	if cfg.LLM.Model != "kimi/kimi-k2.7-code" {
		t.Fatalf("LLM.Model = %q, want kimi/kimi-k2.7-code", cfg.LLM.Model)
	}
}

func TestValidToolName(t *testing.T) {
	for _, name := range []string{"reminder-create", "slack_create", "Tool123"} {
		if !validToolName(name) {
			t.Fatalf("validToolName(%q) = false", name)
		}
	}
	for _, name := range []string{"", "tool name", "tool:*"} {
		if validToolName(name) {
			t.Fatalf("validToolName(%q) = true", name)
		}
	}
}

func TestLoadRejectsDirectKimiCodingEndpoint(t *testing.T) {
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

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "not supported directly") {
		t.Fatalf("Load() error = %v, want direct coding endpoint rejection", err)
	}
}

func TestLoadGitHubConfig(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"MIMO_API_KEY":         "mimo-token",
		"GITHUB_TOKEN":         "github-token",
		"GITHUB_DEFAULT_OWNER": "owner",
		"GITHUB_DEFAULT_REPO":  "repo",
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
	if cfg.Integrations.GitHub.Token != "github-token" {
		t.Fatalf("GitHub token = %q, want github-token", cfg.Integrations.GitHub.Token)
	}
	if cfg.Integrations.GitHub.DefaultOwner != "owner" || cfg.Integrations.GitHub.DefaultRepo != "repo" {
		t.Fatalf("GitHub defaults = %s/%s", cfg.Integrations.GitHub.DefaultOwner, cfg.Integrations.GitHub.DefaultRepo)
	}
}

func TestLoadIntegrationConfig(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":          "xoxb-test",
		"SLACK_SIGNING_SECRET":     "secret",
		"ALLOWED_SLACK_USERS":      "U123",
		"MIMO_API_KEY":             "mimo-token",
		"GITHUB_TOKEN":             "github-token",
		"GITHUB_DEFAULT_OWNER":     "owner",
		"GITHUB_DEFAULT_REPO":      "repo",
		"NOTION_TOKEN":             "notion-token",
		"WEB_SEARCH_PROVIDER":      "brave",
		"WEB_SEARCH_BRAVE_API_KEY": "brave-token",
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
	if cfg.Integrations.GitHub.Token != "github-token" || cfg.Integrations.GitHub.DefaultRepo != "repo" {
		t.Fatalf("GitHub integration config = %#v", cfg.Integrations.GitHub)
	}
	if cfg.Integrations.Notion.Token != "notion-token" {
		t.Fatalf("Notion token = %q, want notion-token", cfg.Integrations.Notion.Token)
	}
	if cfg.Integrations.WebSearch.Provider != "brave" || cfg.Integrations.WebSearch.BraveKey != "brave-token" {
		t.Fatalf("WebSearch integration config = %#v", cfg.Integrations.WebSearch)
	}
}

func TestLoadMaxOutputTokensIsOptional(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"MIMO_API_KEY":         "mimo-token",
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
	if cfg.LLM.MaxOutputTokens != 0 {
		t.Fatalf("MaxOutputTokens = %d, want provider default sentinel 0", cfg.LLM.MaxOutputTokens)
	}

	t.Setenv("LLM_MAX_OUTPUT_TOKEN", "8192")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() with LLM_MAX_OUTPUT_TOKEN error = %v, want nil", err)
	}
	if cfg.LLM.MaxOutputTokens != 8192 {
		t.Fatalf("MaxOutputTokens = %d, want 8192", cfg.LLM.MaxOutputTokens)
	}
}

func TestLoadKeepsWorkspaceAutoFetchOptIn(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"MIMO_API_KEY":         "mimo-token",
		"WORKSPACE_AUTO_FETCH": "false",
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
	if cfg.Security.WorkspaceAutoFetch {
		t.Fatal("WorkspaceAutoFetch = true, want false by default")
	}
}

func TestLoadDefaultsToMiMo(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"MIMO_API_KEY":         "mimo-token",
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
	if cfg.LLM.Provider != "mimo" {
		t.Fatalf("LLM.Provider = %q, want mimo", cfg.LLM.Provider)
	}
	if cfg.LLM.Protocol != "anthropic" {
		t.Fatalf("LLM.Protocol = %q, want anthropic", cfg.LLM.Protocol)
	}
	if cfg.LLM.BaseURL != "https://token-plan-cn.xiaomimimo.com/anthropic" {
		t.Fatalf("LLM.BaseURL = %q, want MiMo base URL", cfg.LLM.BaseURL)
	}
	if cfg.LLM.Model != "mimo-v2.5" {
		t.Fatalf("LLM.Model = %q, want mimo-v2.5", cfg.LLM.Model)
	}
	if cfg.LLM.MultimodalModel != "" {
		t.Fatalf("LLM.MultimodalModel = %q, want empty default", cfg.LLM.MultimodalModel)
	}
	if len(cfg.LLM.MultimodalModels) != 0 {
		t.Fatalf("LLM.MultimodalModels = %#v, want empty default", cfg.LLM.MultimodalModels)
	}
	if cfg.LLM.APIKey != "mimo-token" {
		t.Fatalf("LLM.APIKey = %q, want mimo-token", cfg.LLM.APIKey)
	}
	if cfg.LLM.Thinking != "disabled" {
		t.Fatalf("LLM.Thinking = %q, want disabled for MiMo default", cfg.LLM.Thinking)
	}
}

func TestLoadMiMoTokenPlanKeepsProviderSpecificKey(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"LLM_PROVIDER":         "mimo",
		"MIMO_PROTOCOL":        "anthropic",
		"MIMO_BASE_URL":        "https://token-plan-cn.xiaomimimo.com/anthropic",
		"MIMO_API_KEY":         "tp-token",
		"MIMO_MODEL":           "mimo-v2.5",
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
	if cfg.LLM.Provider != "mimo" || cfg.LLM.Protocol != "anthropic" {
		t.Fatalf("LLM provider/protocol = %s/%s, want mimo/anthropic", cfg.LLM.Provider, cfg.LLM.Protocol)
	}
	if cfg.LLM.APIKey != "tp-token" {
		t.Fatalf("LLM.APIKey = %q, want MiMo token", cfg.LLM.APIKey)
	}
}

func TestLoadOpenAIDoesNotEnableThinkingByDefault(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"LLM_PROVIDER":         "openai",
		"OPENAI_API_KEY":       "openai-token",
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
	if cfg.LLM.Thinking != "" {
		t.Fatalf("LLM.Thinking = %q, want empty for OpenAI default", cfg.LLM.Thinking)
	}
}

func TestLoadDeepSeekDefaults(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"DEEPSEEK_API_KEY":     "ds-token",
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
	if cfg.LLM.Provider != "deepseek" {
		t.Fatalf("LLM.Provider = %q, want deepseek", cfg.LLM.Provider)
	}
	if cfg.LLM.Protocol != "openai" {
		t.Fatalf("LLM.Protocol = %q, want openai", cfg.LLM.Protocol)
	}
	if cfg.LLM.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("LLM.BaseURL = %q, want DeepSeek base URL", cfg.LLM.BaseURL)
	}
	if cfg.LLM.Model != "deepseek-v4-flash" {
		t.Fatalf("LLM.Model = %q, want deepseek-v4-flash", cfg.LLM.Model)
	}
	if cfg.LLM.APIKey != "ds-token" {
		t.Fatalf("LLM.APIKey = %q, want ds-token", cfg.LLM.APIKey)
	}
	if cfg.LLM.Thinking != "" {
		t.Fatalf("LLM.Thinking = %q, want empty DeepSeek default", cfg.LLM.Thinking)
	}
	for _, want := range []string{"deepseek-v4-flash", "deepseek-v4-pro"} {
		if !containsString(cfg.LLM.AvailableModels, want) {
			t.Fatalf("AvailableModels = %#v, want %q", cfg.LLM.AvailableModels, want)
		}
	}
}

func TestLoadOpenCodeGoDefaults(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"OPENCODE_GO_API_KEY":  "oc-go-token",
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
	if cfg.LLM.Provider != "opencode-go" {
		t.Fatalf("LLM.Provider = %q, want opencode-go", cfg.LLM.Provider)
	}
	if cfg.LLM.Protocol != "openai" {
		t.Fatalf("LLM.Protocol = %q, want openai", cfg.LLM.Protocol)
	}
	if cfg.LLM.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("LLM.BaseURL = %q, want OpenCode Go base URL", cfg.LLM.BaseURL)
	}
	if cfg.LLM.Model != "glm-5.2" {
		t.Fatalf("LLM.Model = %q, want glm-5.2", cfg.LLM.Model)
	}
	if cfg.LLM.APIKey != "oc-go-token" {
		t.Fatalf("LLM.APIKey = %q, want oc-go-token", cfg.LLM.APIKey)
	}
	for _, want := range []string{"glm-5.2", "kimi-k2.7-code", "minimax-m3", "qwen3.7-max", "deepseek-v4-flash"} {
		if !containsString(cfg.LLM.AvailableModels, want) {
			t.Fatalf("AvailableModels = %#v, want %q", cfg.LLM.AvailableModels, want)
		}
	}
}

func TestLoadOpenCodeZenDefaults(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"OPENCODE_ZEN_API_KEY": "oc-zen-token",
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
	if cfg.LLM.Provider != "opencode-zen" {
		t.Fatalf("LLM.Provider = %q, want opencode-zen", cfg.LLM.Provider)
	}
	if cfg.LLM.Protocol != "openai" {
		t.Fatalf("LLM.Protocol = %q, want openai", cfg.LLM.Protocol)
	}
	if cfg.LLM.BaseURL != "https://opencode.ai/zen/v1" {
		t.Fatalf("LLM.BaseURL = %q, want OpenCode Zen base URL", cfg.LLM.BaseURL)
	}
	if cfg.LLM.Model != "mimo-v2.5-free" {
		t.Fatalf("LLM.Model = %q, want mimo-v2.5-free", cfg.LLM.Model)
	}
	if cfg.LLM.APIKey != "oc-zen-token" {
		t.Fatalf("LLM.APIKey = %q, want oc-zen-token", cfg.LLM.APIKey)
	}
	for _, want := range []string{"mimo-v2.5-free", "minimax-m3-free", "nemotron-3-ultra-free", "north-mini-code-free"} {
		if !containsString(cfg.LLM.AvailableModels, want) {
			t.Fatalf("AvailableModels = %#v, want %q", cfg.LLM.AvailableModels, want)
		}
	}
}

func TestLoadOpenCodeUsesProviderSpecificAvailableModels(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":              "xoxb-test",
		"SLACK_SIGNING_SECRET":         "secret",
		"ALLOWED_SLACK_USERS":          "U123",
		"LLM_PROVIDER":                 "opencode-go",
		"OPENCODE_GO_API_KEY":          "oc-token",
		"OPENCODE_GO_MODEL":            "glm-5.2",
		"OPENCODE_GO_AVAILABLE_MODELS": "glm-5.2,kimi-k2.7-code,mimo-v2.5",
		"MIMO_AVAILABLE_MODELS":        "mimo-v2.5-pro",
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
	want := []string{"glm-5.2", "kimi-k2.7-code", "mimo-v2.5"}
	if !equalStrings(cfg.LLM.AvailableModels, want) {
		t.Fatalf("AvailableModels = %#v, want %#v", cfg.LLM.AvailableModels, want)
	}
}

func TestLoadOpenCodeZenUsesProviderSpecificAvailableModels(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":               "xoxb-test",
		"SLACK_SIGNING_SECRET":          "secret",
		"ALLOWED_SLACK_USERS":           "U123",
		"LLM_PROVIDER":                  "opencode-zen",
		"OPENCODE_ZEN_API_KEY":          "oc-zen-token",
		"OPENCODE_ZEN_MODEL":            "mimo-v2.5-free",
		"OPENCODE_ZEN_AVAILABLE_MODELS": "mimo-v2.5-free,minimax-m3-free",
		"OPENCODE_GO_AVAILABLE_MODELS":  "glm-5.2,kimi-k2.7-code",
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
	want := []string{"mimo-v2.5-free", "minimax-m3-free"}
	if !equalStrings(cfg.LLM.AvailableModels, want) {
		t.Fatalf("AvailableModels = %#v, want %#v", cfg.LLM.AvailableModels, want)
	}
}

func TestLoadMiMoUsesProviderSpecificAvailableModels(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":              "xoxb-test",
		"SLACK_SIGNING_SECRET":         "secret",
		"ALLOWED_SLACK_USERS":          "U123",
		"LLM_PROVIDER":                 "mimo",
		"MIMO_API_KEY":                 "mimo-token",
		"MIMO_MODEL":                   "mimo-v2.5",
		"MIMO_AVAILABLE_MODELS":        "mimo-v2.5,mimo-v2.5-pro",
		"OPENCODE_GO_AVAILABLE_MODELS": "glm-5.2,kimi-k2.7-code",
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
	want := []string{"mimo-v2.5", "mimo-v2.5-pro"}
	if !equalStrings(cfg.LLM.AvailableModels, want) {
		t.Fatalf("AvailableModels = %#v, want %#v", cfg.LLM.AvailableModels, want)
	}
}

func TestLoadTokenUsageConfigUsesOpenCodeGoEnv(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":          "xoxb-test",
		"SLACK_SIGNING_SECRET":     "secret",
		"ALLOWED_SLACK_USERS":      "U123",
		"LLM_PROVIDER":             "opencode-go",
		"OPENCODE_GO_API_KEY":      "oc-go-token",
		"OPENCODE_GO_WORKSPACE_ID": "wrk_opencode",
		"OPENCODE_GO_AUTH_COOKIE":  "auth=opencode",
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
	if cfg.LLM.TokenUsage.OpenCodeGo.WorkspaceID != "wrk_opencode" {
		t.Fatalf("TokenUsage.OpenCodeGo.WorkspaceID = %q, want OpenCode Go env value", cfg.LLM.TokenUsage.OpenCodeGo.WorkspaceID)
	}
	if cfg.LLM.TokenUsage.OpenCodeGo.AuthCookie != "auth=opencode" {
		t.Fatalf("TokenUsage.OpenCodeGo.AuthCookie = %q, want OpenCode Go env value", cfg.LLM.TokenUsage.OpenCodeGo.AuthCookie)
	}
}

func TestLoadSecondaryModelConfig(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"DEEPSEEK_API_KEY":     "ds-token",
		"SECONDARY_PROVIDER":   "opencode-zen",
		"OPENCODE_ZEN_API_KEY": "secondary-token",
		"SECONDARY_MODEL":      "mimo-v2.5-free",
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
	if cfg.LLM.SecondaryProvider != "opencode-zen" {
		t.Fatalf("SecondaryProvider = %q, want opencode-zen", cfg.LLM.SecondaryProvider)
	}
	if cfg.LLM.SecondaryModel != "mimo-v2.5-free" {
		t.Fatalf("SecondaryModel = %q, want mimo-v2.5-free", cfg.LLM.SecondaryModel)
	}
	if cfg.LLM.SecondaryAPIKey != "secondary-token" {
		t.Fatalf("SecondaryAPIKey = %q, want secondary-token", cfg.LLM.SecondaryAPIKey)
	}
}

func TestLoadOpenCodeGoAnthropicEndpoint(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"LLM_PROVIDER":         "opencode-go",
		"OPENCODE_GO_PROTOCOL": "anthropic",
		"OPENCODE_GO_API_KEY":  "oc-go-token",
		"OPENCODE_GO_MODEL":    "minimax-m3",
		"OPENCODE_GO_BASE_URL": "https://opencode.ai/zen/go/v1",
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
	if cfg.LLM.Provider != "opencode-go" || cfg.LLM.Protocol != "anthropic" {
		t.Fatalf("LLM provider/protocol = %s/%s, want opencode-go/anthropic", cfg.LLM.Provider, cfg.LLM.Protocol)
	}
	if cfg.LLM.Model != "minimax-m3" {
		t.Fatalf("LLM.Model = %q, want minimax-m3", cfg.LLM.Model)
	}
}

func TestLoadRejectsDirectKimiCodingEndpointRegardlessOfProtocol(t *testing.T) {
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
	if err == nil || !strings.Contains(err.Error(), "not supported directly") {
		t.Fatalf("Load() error = %v, want direct coding endpoint rejection", err)
	}
}

func TestLoadWebSearchConfig(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":             "xoxb-test",
		"SLACK_SIGNING_SECRET":        "secret",
		"ALLOWED_SLACK_USERS":         "U123",
		"LLM_PROVIDER":                "openai",
		"OPENAI_API_KEY":              "openai-token",
		"WEB_SEARCH_PROVIDER":         "serpapi",
		"WEB_SEARCH_SERPAPI_KEY":      "serp-key",
		"WEB_SEARCH_SERPAPI_BASE_URL": "https://serpapi.example/search.json",
		"WEB_SEARCH_BRAVE_API_KEY":    "brave-key",
		"WEB_SEARCH_BRAVE_BASE_URL":   "https://brave.example/res/v1/web/search",
	})

	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	webSearch := cfg.Integrations.WebSearch
	if webSearch.Provider != "serpapi" || webSearch.SerpAPIKey != "serp-key" {
		t.Fatalf("web search config = %#v", webSearch)
	}
	if webSearch.SerpAPIURL != "https://serpapi.example/search.json" {
		t.Fatalf("SerpAPIURL = %q", webSearch.SerpAPIURL)
	}
	if webSearch.BraveKey != "brave-key" {
		t.Fatalf("BraveKey = %q", webSearch.BraveKey)
	}
	if webSearch.BraveURL != "https://brave.example/res/v1/web/search" {
		t.Fatalf("BraveURL = %q", webSearch.BraveURL)
	}
}

func TestLoadLuckinMCPConfig(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"MIMO_API_KEY":         "mimo-token",
		"LUCKIN_MCP_URL":       "https://example.test/mcp/",
		"LUCKIN_MCP_TOKEN":     "luckin-token",
	})

	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Integrations.Luckin.MCPURL != "https://example.test/mcp" {
		t.Fatalf("LuckinMCPURL = %q", cfg.Integrations.Luckin.MCPURL)
	}
	if cfg.Integrations.Luckin.MCPToken != "luckin-token" {
		t.Fatalf("LuckinMCPToken = %q", cfg.Integrations.Luckin.MCPToken)
	}
}

func TestLoadObservabilityAuthConfig(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":                     "xoxb-test",
		"SLACK_SIGNING_SECRET":                "secret",
		"ALLOWED_SLACK_USERS":                 "U123",
		"MIMO_API_KEY":                        "mimo-token",
		"OBSERVABILITY_TOKEN":                 "admin-token",
		"OBSERVABILITY_ALLOW_UNAUTHENTICATED": "true",
	})

	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Observing.AdminToken != "admin-token" {
		t.Fatalf("AdminToken = %q, want admin-token", cfg.Observing.AdminToken)
	}
	if !cfg.Observing.AllowUnauthenticated {
		t.Fatal("AllowUnauthenticated = false, want true")
	}
}

func TestLoadForGatewayAllowsMinimalIngressSecrets(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFileNamed(t, dir, filepath.Join("gateway", ".env"), map[string]string{
		"SLACK_SIGNING_SECRET": "secret",
	})

	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFor(ProfileGateway)
	if err != nil {
		t.Fatalf("LoadFor(ProfileGateway) error = %v", err)
	}
	if cfg.Slack.SigningSecret != "secret" {
		t.Fatalf("SigningSecret = %q, want secret", cfg.Slack.SigningSecret)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SLACK_BOT_TOKEN") {
		t.Fatalf("Load() error = %v, want full runtime to require SLACK_BOT_TOKEN", err)
	}
}

func TestLoadForObservabilityAllowsStorageOnly(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFileNamed(t, dir, filepath.Join("observability", ".env"), map[string]string{})

	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadFor(ProfileObservability); err != nil {
		t.Fatalf("LoadFor(ProfileObservability) error = %v", err)
	}
}

func TestLoadForUsesProfileEnvFile(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "worker-secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"MIMO_API_KEY":         "mimo-token",
	})
	writeEnvFileNamed(t, dir, filepath.Join("gateway", ".env"), map[string]string{
		"SLACK_SIGNING_SECRET": "gateway-secret",
	})

	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFor(ProfileGateway)
	if err != nil {
		t.Fatalf("LoadFor(ProfileGateway) error = %v", err)
	}
	if cfg.Slack.SigningSecret != "gateway-secret" {
		t.Fatalf("SigningSecret = %q, want gateway-secret", cfg.Slack.SigningSecret)
	}
}

func TestLoadForSlackWorkerUsesWorkerEnvFile(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFileNamed(t, dir, filepath.Join("worker", ".env"), map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-worker",
		"SLACK_SIGNING_SECRET": "worker-secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"MIMO_API_KEY":         "mimo-token",
	})

	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFor(ProfileSlackWorker)
	if err != nil {
		t.Fatalf("LoadFor(ProfileSlackWorker) error = %v", err)
	}
	if cfg.Slack.BotToken != "xoxb-worker" || cfg.Slack.SigningSecret != "worker-secret" {
		t.Fatalf("Slack config = %#v", cfg.Slack)
	}
}

func TestLoadForHonorsExplicitEnvFile(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFileNamed(t, dir, "custom.env", map[string]string{
		"SLACK_SIGNING_SECRET": "custom-secret",
	})
	t.Setenv("SLACK_COPILOT_ENV_FILE", filepath.Join(dir, "custom.env"))

	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFor(ProfileGateway)
	if err != nil {
		t.Fatalf("LoadFor(ProfileGateway) error = %v", err)
	}
	if cfg.Slack.SigningSecret != "custom-secret" {
		t.Fatalf("SigningSecret = %q, want custom-secret", cfg.Slack.SigningSecret)
	}
}

func writeEnvFile(t *testing.T, dir string, values map[string]string) {
	writeEnvFileNamed(t, dir, filepath.Join("worker", ".env"), values)
}

func writeEnvFileNamed(t *testing.T, dir, name string, values map[string]string) {
	t.Helper()
	if _, ok := values["POSTGRES_DSN"]; !ok {
		values["POSTGRES_DSN"] = "postgres://test:test@localhost:5432/slack_copilot?sslmode=disable"
	}
	if _, ok := values["REDIS_URL"]; !ok {
		values["REDIS_URL"] = "redis://localhost:6379/0"
	}
	lines := make([]string, 0, len(values))
	for key, value := range values {
		lines = append(lines, key+"="+value)
	}
	data := strings.Join(lines, "\n") + "\n"
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
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
		"LLM_PROVIDER",
		"LLM_ANTHROPIC_FLAVOR",
		"MIMO_PROTOCOL",
		"MIMO_API_KEY",
		"MIMO_BASE_URL",
		"MIMO_MODEL",
		"MIMO_AVAILABLE_MODELS",
		"MIMO_THINKING",
		"MIMO_TEMPERATURE",
		"MIMO_TIMEOUT",
		"MODEL_ROUTING_MULTIMODAL_MODEL",
		"MULTIMODAL_MODEL",
		"MULTIMODAL_MODELS",
		"KIMI_PROTOCOL",
		"CLIPROXYAPI_PROTOCOL",
		"CLIPROXYAPI_API_KEY",
		"CLIPROXYAPI_BASE_URL",
		"CLIPROXYAPI_MODEL",
		"CLIPROXYAPI_AVAILABLE_MODELS",
		"CLIPROXYAPI_THINKING",
		"CLIPROXYAPI_TEMPERATURE",
		"CLIPROXYAPI_TIMEOUT",
		"ANTHROPIC_PROTOCOL",
		"ANTHROPIC_FLAVOR",
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OPENAI_MODEL",
		"DEEPSEEK_PROTOCOL",
		"DEEPSEEK_API_KEY",
		"DEEPSEEK_BASE_URL",
		"DEEPSEEK_MODEL",
		"DEEPSEEK_AVAILABLE_MODELS",
		"DEEPSEEK_THINKING",
		"DEEPSEEK_TEMPERATURE",
		"DEEPSEEK_TIMEOUT",
		"KIMI_API_KEY",
		"KIMI_BASE_URL",
		"KIMI_MODEL",
		"KIMI_AVAILABLE_MODELS",
		"OPENCODE_GO_PROTOCOL",
		"OPENCODE_GO_API_KEY",
		"OPENCODE_GO_BASE_URL",
		"OPENCODE_GO_MODEL",
		"OPENCODE_GO_AVAILABLE_MODELS",
		"OPENCODE_GO_TEMPERATURE",
		"OPENCODE_GO_TIMEOUT",
		"OPENCODE_GO_WORKSPACE_ID",
		"OPENCODE_GO_AUTH_COOKIE",
		"OPENCODE_ZEN_PROTOCOL",
		"OPENCODE_ZEN_API_KEY",
		"OPENCODE_ZEN_BASE_URL",
		"OPENCODE_ZEN_MODEL",
		"OPENCODE_ZEN_AVAILABLE_MODELS",
		"OPENCODE_ZEN_TEMPERATURE",
		"OPENCODE_ZEN_TIMEOUT",
		"MOONSHOT_API_KEY",
		"MOONSHOT_BASE_URL",
		"MOONSHOT_MODEL",
		"MOONSHOT_AVAILABLE_MODELS",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_MODEL",
		"ANTHROPIC_AVAILABLE_MODELS",
		"OPENAI_AVAILABLE_MODELS",
		"LLM_MAX_OUTPUT_TOKEN",
		"POSTGRES_DSN",
		"REDIS_URL",
		"SLACK_EVENT_MAX_ATTEMPTS",
		"SLACK_EVENT_RETRY_BASE",
		"SLACK_EVENT_RETRY_MAX",
		"AGENT_ALLOWED_WRITE_TOOLS",
		"WORKSPACE_AUTO_FETCH",
		"GCLOUD_PATH",
		"GCP_PROJECT",
		"GCP_NAMESPACE",
		"GKE_CLUSTER",
		"GKE_REGION",
		"KUBECTL_PATH",
		"K8S_DEFAULT_CONTEXT",
		"K8S_DEFAULT_CLUSTER",
		"K8S_DEFAULT_NAMESPACE",
		"TTS_API_KEY",
		"TTS_BASE_URL",
		"TTS_MODEL",
		"TTS_DEFAULT_VOICE",
		"TTS_DEFAULT_STYLE",
		"GITHUB_TOKEN",
		"GITHUB_API_BASE_URL",
		"GITHUB_DEFAULT_OWNER",
		"GITHUB_DEFAULT_REPO",
		"NOTION_TOKEN",
		"NOTION_DATABASE_ID",
		"NOTION_TITLE_PROPERTY",
		"NOTION_VERSION",
		"YOUTRACK_URL",
		"YOUTRACK_TOKEN",
		"LUCKIN_MCP_URL",
		"LUCKIN_MCP_TOKEN",
		"PLAYWRIGHT_MCP_URL",
		"PLAYWRIGHT_MCP_TOKEN",
		"WEB_SEARCH_PROVIDER",
		"WEB_SEARCH_GOOGLE_API_KEY",
		"WEB_SEARCH_GOOGLE_CX",
		"WEB_SEARCH_SERPAPI_KEY",
		"WEB_SEARCH_SERPAPI_BASE_URL",
		"WEB_SEARCH_SEARXNG_URL",
		"WEB_SEARCH_BRAVE_API_KEY",
		"WEB_SEARCH_BRAVE_BASE_URL",
		"OBSERVABILITY_TOKEN",
		"OBSERVABILITY_ALLOW_UNAUTHENTICATED",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

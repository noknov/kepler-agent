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
	if cfg.Tools.GitHubToken != "github-token" {
		t.Fatalf("GitHubToken = %q, want github-token", cfg.Tools.GitHubToken)
	}
	if cfg.Tools.GitHubDefaultOwner != "owner" || cfg.Tools.GitHubDefaultRepo != "repo" {
		t.Fatalf("GitHub defaults = %s/%s", cfg.Tools.GitHubDefaultOwner, cfg.Tools.GitHubDefaultRepo)
	}
}

func TestLoadRAGBackgroundIndexDefaultsOff(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"MIMO_API_KEY":         "mimo-token",
		"RAG_ENABLED":          "true",
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
	if cfg.RAG.BackgroundIndex {
		t.Fatal("RAG.BackgroundIndex = true, want false by default")
	}
}

func TestLoadRAGBackgroundIndexCanBeEnabled(t *testing.T) {
	resetConfigEnv(t)
	dir := t.TempDir()
	writeEnvFile(t, dir, map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_SIGNING_SECRET": "secret",
		"ALLOWED_SLACK_USERS":  "U123",
		"MIMO_API_KEY":         "mimo-token",
		"RAG_BACKGROUND_INDEX": "true",
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
	if !cfg.RAG.BackgroundIndex {
		t.Fatal("RAG.BackgroundIndex = false, want true")
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
	if cfg.Tools.WebSearchProvider != "serpapi" || cfg.Tools.WebSearchSerpAPIKey != "serp-key" {
		t.Fatalf("web search config = %#v", cfg.Tools)
	}
	if cfg.Tools.WebSearchSerpAPIURL != "https://serpapi.example/search.json" {
		t.Fatalf("WebSearchSerpAPIURL = %q", cfg.Tools.WebSearchSerpAPIURL)
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
	if cfg.Tools.LuckinMCPURL != "https://example.test/mcp" {
		t.Fatalf("LuckinMCPURL = %q", cfg.Tools.LuckinMCPURL)
	}
	if cfg.Tools.LuckinMCPToken != "luckin-token" {
		t.Fatalf("LuckinMCPToken = %q", cfg.Tools.LuckinMCPToken)
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
		"LLM_PROVIDER",
		"LLM_VENDOR",
		"LLM_ANTHROPIC_FLAVOR",
		"MIMO_PROTOCOL",
		"MIMO_API_KEY",
		"MIMO_BASE_URL",
		"MIMO_MODEL",
		"MIMO_THINKING",
		"MIMO_MAX_TOKENS",
		"MIMO_TEMPERATURE",
		"MIMO_TIMEOUT",
		"KIMI_PROTOCOL",
		"ANTHROPIC_PROTOCOL",
		"ANTHROPIC_FLAVOR",
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OPENAI_MODEL",
		"KIMI_API_KEY",
		"KIMI_BASE_URL",
		"KIMI_MODEL",
		"OPENCODE_GO_PROTOCOL",
		"OPENCODE_GO_API_KEY",
		"OPENCODE_GO_BASE_URL",
		"OPENCODE_GO_MODEL",
		"OPENCODE_GO_MAX_TOKENS",
		"OPENCODE_GO_TEMPERATURE",
		"OPENCODE_GO_TIMEOUT",
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
		"WORKSPACE_AUTO_FETCH",
		"PROMPT_INCLUDE_REPO_INVENTORY",
		"GITHUB_TOKEN",
		"GITHUB_API_BASE_URL",
		"GITHUB_DEFAULT_OWNER",
		"GITHUB_DEFAULT_REPO",
		"LUCKIN_MCP_URL",
		"LUCKIN_MCP_TOKEN",
		"WEB_SEARCH_PROVIDER",
		"WEB_SEARCH_GOOGLE_API_KEY",
		"WEB_SEARCH_GOOGLE_CX",
		"WEB_SEARCH_SERPAPI_KEY",
		"WEB_SEARCH_SERPAPI_BASE_URL",
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

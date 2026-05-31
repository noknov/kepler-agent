package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTP      HTTPConfig
	Slack     SlackConfig
	LLM       LLMConfig
	Security  SecurityConfig
	Sessions  SessionConfig
	Tools     ToolConfig
	Observing ObservingConfig
}

type HTTPConfig struct {
	Addr string
}

type SlackConfig struct {
	BotToken      string
	SigningSecret string
	BotUserID     string
}

type LLMConfig struct {
	BaseURL         string
	APIKey          string
	Model           string
	Protocol        string
	AnthropicFlavor string
	Thinking        string
	MaxTokens       int
	Temperature     float64
	Timeout         time.Duration
}

type SecurityConfig struct {
	AllowedUsers    []string
	AllowedChannels []string
	WorkspaceRoots  []string
}

type SessionConfig struct {
	DataDir         string
	MaxMessages     int
	MaxToolChars    int
	MaxThreadChars  int
	MaxSummaryChars int
}

type ToolConfig struct {
	CommandTimeout      time.Duration
	GCloudPath          string
	GCPDefaultProject   string
	GCPDefaultNamespace string
	GKEDefaultCluster   string
	GKEDefaultRegion    string
	NotionToken         string
	NotionDatabaseID    string
	NotionTitleProperty string
	NotionVersion       string
	YouTrackURL         string
	YouTrackToken       string
	GitHubToken         string
	GitHubAPIBaseURL    string
	GitHubDefaultOwner  string
	GitHubDefaultRepo   string
}

type ObservingConfig struct {
	LogLevel string
}

func Load() (Config, error) {
	dotenvValues, err := readDotEnv(".env")
	if err != nil {
		return Config{}, err
	}
	allowEnvMixing := envBoolValue(firstNonEmpty(os.Getenv("ALLOW_ENV_MIXING"), dotenvValues["ALLOW_ENV_MIXING"]))
	preferDotEnv := envBoolValue(firstNonEmpty(os.Getenv("PREFER_DOTENV"), dotenvValues["PREFER_DOTENV"]))
	conflicts := providerEnvConflicts(dotenvValues)
	if len(conflicts) > 0 && !allowEnvMixing && !preferDotEnv {
		return Config{}, fmt.Errorf(".env conflicts with existing shell environment for %s; clear the shell variables, set PREFER_DOTENV=true to use .env, or set ALLOW_ENV_MIXING=true", strings.Join(conflicts, ", "))
	}
	applyDotEnv(dotenvValues, preferDotEnv)
	wd, _ := os.Getwd()
	llmProtocol := normalizeLLMProtocol(firstEnv("LLM_PROTOCOL", "MIMO_PROTOCOL", "KIMI_PROTOCOL", "ANTHROPIC_PROTOCOL"))
	llmBaseURL := providerBaseURL(llmProtocol)
	if llmBaseURL == "" {
		llmBaseURL = os.Getenv("ANTHROPIC_BASE_URL")
		if llmBaseURL != "" && llmProtocol == "" {
			llmProtocol = "anthropic"
		}
	}
	if llmBaseURL == "" {
		llmBaseURL = "https://api.xiaomimimo.com/v1"
	}
	llmBaseURL = normalizeLLMBaseURL(llmBaseURL, llmProtocol)
	if llmProtocol == "" {
		llmProtocol = inferLLMProtocol(llmBaseURL)
	}
	anthropicFlavor := normalizeAnthropicFlavor(firstEnv("LLM_ANTHROPIC_FLAVOR", "ANTHROPIC_FLAVOR"))
	if anthropicFlavor == "" && llmProtocol == "anthropic" {
		anthropicFlavor = inferAnthropicFlavor(llmBaseURL)
	}
	llmModel := providerModel(llmProtocol)
	if llmModel == "" && strings.Contains(llmBaseURL, "api.kimi.com/coding") {
		llmModel = "kimi-for-coding"
	}
	if llmModel == "" {
		llmModel = "mimo-v2.5"
	}
	llmThinking := firstEnv("MIMO_THINKING", "KIMI_THINKING")
	if llmThinking == "" {
		llmThinking = "enabled"
	}
	if isMiMoConfig(llmBaseURL, llmModel) && firstEnv("MIMO_THINKING", "KIMI_THINKING") == "" {
		llmThinking = "disabled"
	}
	cfg := Config{
		HTTP: HTTPConfig{
			Addr: env("HTTP_ADDR", ":8080"),
		},
		Slack: SlackConfig{
			BotToken:      os.Getenv("SLACK_BOT_TOKEN"),
			SigningSecret: os.Getenv("SLACK_SIGNING_SECRET"),
			BotUserID:     os.Getenv("SLACK_BOT_USER_ID"),
		},
		LLM: LLMConfig{
			BaseURL:         trimRightSlash(llmBaseURL),
			APIKey:          providerAPIKey(llmProtocol, llmBaseURL, llmModel),
			Model:           llmModel,
			Protocol:        llmProtocol,
			AnthropicFlavor: anthropicFlavor,
			Thinking:        llmThinking,
			MaxTokens:       envIntAliases(8192, "MIMO_MAX_TOKENS", "KIMI_MAX_TOKENS", "CLAUDE_CODE_MAX_OUTPUT_TOKENS"),
			Temperature:     envFloatAliases(0.2, "MIMO_TEMPERATURE", "KIMI_TEMPERATURE"),
			Timeout:         envDurationAliases(120*time.Second, "MIMO_TIMEOUT", "KIMI_TIMEOUT", "API_TIMEOUT_MS"),
		},
		Security: SecurityConfig{
			AllowedUsers:    envCSV("ALLOWED_SLACK_USERS"),
			AllowedChannels: envCSV("ALLOWED_SLACK_CHANNELS"),
			WorkspaceRoots:  normalizeRoots(envCSVDefault("WORKSPACE_ROOTS", []string{wd})),
		},
		Sessions: SessionConfig{
			DataDir:         env("SESSION_DATA_DIR", filepath.Join(wd, ".data", "sessions")),
			MaxMessages:     envInt("SESSION_MAX_MESSAGES", 24),
			MaxToolChars:    envInt("SESSION_MAX_TOOL_CHARS", 3500),
			MaxThreadChars:  envInt("SESSION_MAX_THREAD_CHARS", 6000),
			MaxSummaryChars: envInt("SESSION_MAX_SUMMARY_CHARS", 2500),
		},
		Tools: ToolConfig{
			CommandTimeout:      envDuration("TOOL_COMMAND_TIMEOUT", 30*time.Second),
			GCloudPath:          env("GCLOUD_PATH", "gcloud"),
			GCPDefaultProject:   os.Getenv("GCP_PROJECT"),
			GCPDefaultNamespace: env("GCP_NAMESPACE", ""),
			GKEDefaultCluster:   env("GKE_CLUSTER", ""),
			GKEDefaultRegion:    env("GKE_REGION", ""),
			NotionToken:         os.Getenv("NOTION_TOKEN"),
			NotionDatabaseID:    os.Getenv("NOTION_DATABASE_ID"),
			NotionTitleProperty: env("NOTION_TITLE_PROPERTY", "Name"),
			NotionVersion:       env("NOTION_VERSION", "2022-06-28"),
			YouTrackURL:         trimRightSlash(os.Getenv("YOUTRACK_URL")),
			YouTrackToken:       os.Getenv("YOUTRACK_TOKEN"),
			GitHubToken:         os.Getenv("GITHUB_TOKEN"),
			GitHubAPIBaseURL:    trimRightSlash(env("GITHUB_API_BASE_URL", "https://api.github.com")),
			GitHubDefaultOwner:  os.Getenv("GITHUB_DEFAULT_OWNER"),
			GitHubDefaultRepo:   os.Getenv("GITHUB_DEFAULT_REPO"),
		},
		Observing: ObservingConfig{
			LogLevel: env("LOG_LEVEL", "info"),
		},
	}

	if cfg.Slack.SigningSecret == "" {
		return cfg, fmt.Errorf("SLACK_SIGNING_SECRET is required")
	}
	if cfg.Slack.BotToken == "" {
		return cfg, fmt.Errorf("SLACK_BOT_TOKEN is required")
	}
	if strings.Contains(cfg.LLM.BaseURL, "api.kimi.com/coding") && cfg.LLM.Protocol != "anthropic" && !envBool("ALLOW_EXPERIMENTAL_CODING_ENDPOINT", false) {
		return cfg, fmt.Errorf("the coding endpoint %q is disabled for oncall-agent by default; switch to an OpenAI-compatible provider or set ALLOW_EXPERIMENTAL_CODING_ENDPOINT=true to continue deliberately", cfg.LLM.BaseURL)
	}
	if cfg.LLM.APIKey == "" {
		return cfg, fmt.Errorf("MIMO_API_KEY, MOONSHOT_API_KEY, KIMI_API_KEY, OPENAI_API_KEY, ANTHROPIC_API_KEY, or ANTHROPIC_AUTH_TOKEN is required")
	}
	if cfg.LLM.Protocol != "openai" && cfg.LLM.Protocol != "anthropic" {
		return cfg, fmt.Errorf("LLM_PROTOCOL must be openai or anthropic")
	}
	if cfg.LLM.AnthropicFlavor != "" && cfg.LLM.AnthropicFlavor != "official" && cfg.LLM.AnthropicFlavor != "claude-code" {
		return cfg, fmt.Errorf("LLM_ANTHROPIC_FLAVOR must be official or claude-code")
	}
	if len(cfg.Security.AllowedUsers) == 0 {
		return cfg, fmt.Errorf("ALLOWED_SLACK_USERS is required")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func providerBaseURL(protocol string) string {
	if protocol == "anthropic" {
		return firstEnv("ANTHROPIC_BASE_URL", "MIMO_BASE_URL", "KIMI_BASE_URL", "MOONSHOT_BASE_URL", "OPENAI_BASE_URL")
	}
	return firstEnv("MIMO_BASE_URL", "KIMI_BASE_URL", "MOONSHOT_BASE_URL", "OPENAI_BASE_URL")
}

func providerModel(protocol string) string {
	if protocol == "anthropic" {
		return firstEnv("ANTHROPIC_MODEL", "MIMO_MODEL", "KIMI_MODEL", "MOONSHOT_MODEL", "OPENAI_MODEL")
	}
	return firstEnv("MIMO_MODEL", "KIMI_MODEL", "MOONSHOT_MODEL", "OPENAI_MODEL", "ANTHROPIC_MODEL")
}

func providerAPIKey(protocol, baseURL, model string) string {
	if isMiMoConfig(baseURL, model) {
		return firstEnv("MIMO_API_KEY")
	}
	if protocol == "anthropic" {
		return firstEnv("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "MIMO_API_KEY", "MOONSHOT_API_KEY", "KIMI_API_KEY", "OPENAI_API_KEY")
	}
	return firstEnv("MIMO_API_KEY", "MOONSHOT_API_KEY", "KIMI_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN")
}

func envCSV(key string) []string {
	return envCSVDefault(key, nil)
}

func envCSVDefault(key string, fallback []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func envIntAliases(fallback int, keys ...string) int {
	for _, key := range keys {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		v, err := strconv.Atoi(raw)
		if err == nil {
			return v
		}
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return v
}

func envFloatAliases(fallback float64, keys ...string) float64 {
	for _, key := range keys {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err == nil {
			return v
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}

func envDurationAliases(fallback time.Duration, keys ...string) time.Duration {
	for _, key := range keys {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		if strings.HasSuffix(key, "_MS") {
			if ms, err := strconv.Atoi(raw); err == nil {
				return time.Duration(ms) * time.Millisecond
			}
		}
		if d, err := time.ParseDuration(raw); err == nil {
			return d
		}
		if seconds, err := strconv.Atoi(raw); err == nil {
			return time.Duration(seconds) * time.Second
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	return envBoolValue(raw)
}

func envBoolValue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func normalizeRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		out = append(out, filepath.Clean(abs))
	}
	return out
}

func trimRightSlash(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), "/")
}

func normalizeLLMProtocol(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func normalizeLLMBaseURL(raw, protocol string) string {
	raw = trimRightSlash(raw)
	if raw == "" {
		return ""
	}
	if protocol != "anthropic" && strings.Contains(raw, "api.kimi.com/coding") && !strings.HasSuffix(raw, "/v1") {
		return raw + "/v1"
	}
	return raw
}

func inferLLMProtocol(raw string) string {
	raw = trimRightSlash(raw)
	if strings.Contains(raw, "api.kimi.com/coding") && !strings.HasSuffix(raw, "/v1") {
		return "anthropic"
	}
	return "openai"
}

func normalizeAnthropicFlavor(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "claudecode", "claude_code", "claude-code":
		return "claude-code"
	default:
		return raw
	}
}

func inferAnthropicFlavor(raw string) string {
	if strings.Contains(trimRightSlash(raw), "api.kimi.com/coding") {
		return "claude-code"
	}
	return "official"
}

func isMiMoConfig(baseURL, model string) bool {
	baseURL = strings.ToLower(trimRightSlash(baseURL))
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(baseURL, "xiaomimimo.com") || strings.HasPrefix(model, "mimo-")
}

func readDotEnv(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		values[key] = value
	}
	return values, scanner.Err()
}

func applyDotEnv(values map[string]string, overwrite bool) {
	for key, value := range values {
		if os.Getenv(key) != "" && !overwrite {
			continue
		}
		_ = os.Setenv(key, value)
	}
}

func providerEnvConflicts(dotenvValues map[string]string) []string {
	shell := providerSnapshot(func(key string) string { return os.Getenv(key) })
	dotenv := providerSnapshot(func(key string) string { return dotenvValues[key] })
	if !shell.configured() || !dotenv.configured() {
		return nil
	}

	conflicts := make([]string, 0, 4)
	if shell.Protocol != dotenv.Protocol {
		conflicts = append(conflicts, "LLM_PROTOCOL")
	}
	if shell.AnthropicFlavor != dotenv.AnthropicFlavor {
		conflicts = append(conflicts, "LLM_ANTHROPIC_FLAVOR")
	}
	if shell.BaseURL != dotenv.BaseURL {
		conflicts = append(conflicts, "LLM_BASE_URL")
	}
	if shell.Model != dotenv.Model {
		conflicts = append(conflicts, "LLM_MODEL")
	}
	if shell.APIKey != dotenv.APIKey {
		conflicts = append(conflicts, "LLM_API_KEY")
	}
	sort.Strings(conflicts)
	return conflicts
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type llmEnvSnapshot struct {
	Protocol        string
	AnthropicFlavor string
	BaseURL         string
	Model           string
	APIKey          string
}

func (s llmEnvSnapshot) configured() bool {
	return s.Protocol != "" || s.AnthropicFlavor != "" || s.BaseURL != "" || s.Model != "" || s.APIKey != ""
}

func providerSnapshot(get func(string) string) llmEnvSnapshot {
	protocol := normalizeLLMProtocol(firstNonEmpty(
		get("LLM_PROTOCOL"),
		get("MIMO_PROTOCOL"),
		get("KIMI_PROTOCOL"),
		get("ANTHROPIC_PROTOCOL"),
	))
	anthropicFlavor := normalizeAnthropicFlavor(firstNonEmpty(
		get("LLM_ANTHROPIC_FLAVOR"),
		get("ANTHROPIC_FLAVOR"),
	))
	baseURL := firstNonEmpty(
		get("MIMO_BASE_URL"),
		get("KIMI_BASE_URL"),
		get("MOONSHOT_BASE_URL"),
		get("OPENAI_BASE_URL"),
		get("ANTHROPIC_BASE_URL"),
	)
	if baseURL != "" && protocol == "" && strings.TrimSpace(get("ANTHROPIC_BASE_URL")) != "" &&
		strings.TrimSpace(get("MIMO_BASE_URL")) == "" &&
		strings.TrimSpace(get("KIMI_BASE_URL")) == "" &&
		strings.TrimSpace(get("MOONSHOT_BASE_URL")) == "" &&
		strings.TrimSpace(get("OPENAI_BASE_URL")) == "" {
		protocol = "anthropic"
	}
	baseURL = normalizeLLMBaseURL(baseURL, protocol)
	if protocol == "" && baseURL != "" {
		protocol = inferLLMProtocol(baseURL)
	}
	if anthropicFlavor == "" && protocol == "anthropic" {
		anthropicFlavor = inferAnthropicFlavor(baseURL)
	}
	snapshot := llmEnvSnapshot{
		Protocol:        protocol,
		AnthropicFlavor: anthropicFlavor,
		BaseURL:         trimRightSlash(baseURL),
		Model: firstNonEmpty(
			get("MIMO_MODEL"),
			get("KIMI_MODEL"),
			get("MOONSHOT_MODEL"),
			get("OPENAI_MODEL"),
			get("ANTHROPIC_MODEL"),
		),
		APIKey: firstNonEmpty(
			get("MIMO_API_KEY"),
			get("MOONSHOT_API_KEY"),
			get("KIMI_API_KEY"),
			get("OPENAI_API_KEY"),
			get("ANTHROPIC_API_KEY"),
			get("ANTHROPIC_AUTH_TOKEN"),
		),
	}
	return snapshot
}

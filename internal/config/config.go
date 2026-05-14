package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
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
	BaseURL     string
	APIKey      string
	Model       string
	Thinking    string
	MaxTokens   int
	Temperature float64
	Timeout     time.Duration
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
}

type ObservingConfig struct {
	LogLevel string
}

func Load() (Config, error) {
	_ = loadDotEnv(".env")
	wd, _ := os.Getwd()
	llmBaseURL := firstEnv("KIMI_BASE_URL", "MOONSHOT_BASE_URL", "OPENAI_BASE_URL")
	if llmBaseURL == "" {
		llmBaseURL = normalizeClaudeCodeBaseURL(os.Getenv("ANTHROPIC_BASE_URL"))
	}
	if llmBaseURL == "" {
		llmBaseURL = "https://api.moonshot.ai/v1"
	}
	llmModel := firstEnv("KIMI_MODEL", "MOONSHOT_MODEL", "OPENAI_MODEL")
	if llmModel == "" {
		llmModel = os.Getenv("ANTHROPIC_MODEL")
	}
	if llmModel == "" && strings.Contains(llmBaseURL, "api.kimi.com/coding") {
		llmModel = "kimi-for-coding"
	}
	if llmModel == "" {
		llmModel = "kimi-k2.6"
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
			BaseURL:     trimRightSlash(llmBaseURL),
			APIKey:      firstEnv("MOONSHOT_API_KEY", "KIMI_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"),
			Model:       llmModel,
			Thinking:    env("KIMI_THINKING", "enabled"),
			MaxTokens:   envIntAliases(8192, "KIMI_MAX_TOKENS", "CLAUDE_CODE_MAX_OUTPUT_TOKENS"),
			Temperature: envFloat("KIMI_TEMPERATURE", 0.2),
			Timeout:     envDurationAliases(120*time.Second, "KIMI_TIMEOUT", "API_TIMEOUT_MS"),
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
	if cfg.LLM.APIKey == "" {
		return cfg, fmt.Errorf("MOONSHOT_API_KEY, KIMI_API_KEY, OPENAI_API_KEY, ANTHROPIC_API_KEY, or ANTHROPIC_AUTH_TOKEN is required")
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

func normalizeClaudeCodeBaseURL(raw string) string {
	raw = trimRightSlash(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "api.kimi.com/coding") && !strings.HasSuffix(raw, "/v1") {
		return raw + "/v1"
	}
	return raw
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

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
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		_ = os.Setenv(key, value)
	}
	return scanner.Err()
}

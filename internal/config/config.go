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
	Storage   StorageConfig
}

// StorageConfig owns all durable operational state for sessions, runs, inbox,
// and reminders.
type StorageConfig struct {
	PostgresDSN string
	RedisURL    string
}

type HTTPConfig struct {
	Addr                string
	EventWorkers        int
	EventQueueSize      int
	EventEnqueueTimeout time.Duration
	EventTimeout        time.Duration
	EventInboxLease     time.Duration
	ShutdownTimeout     time.Duration
}

type SlackConfig struct {
	BotToken      string
	SigningSecret string
	BotUserID     string
}

type LLMConfig struct {
	Provider         string
	BaseURL          string
	APIKey           string
	Model            string
	AvailableModels  []string
	MultimodalModel  string
	MultimodalModels []string
	TokenUsage       TokenUsageConfig
	Protocol         string
	AnthropicFlavor  string
	Thinking         string
	Temperature      float64
	Timeout          time.Duration

	// Secondary model is used for cheaper/faster background work such as
	// read-only code exploration and compact summaries.
	SecondaryProvider string
	SecondaryBaseURL  string
	SecondaryAPIKey   string
	SecondaryModel    string
	SecondaryProtocol string

	// DynamicStatus enables async secondary-model status summaries that
	// replace the static tool hints with a short description of what the
	// current tool call is actually doing.
	DynamicStatus bool
}

type TokenUsageConfig struct {
	OpenCodeGo OpenCodeGoTokenUsageConfig
}

type OpenCodeGoTokenUsageConfig struct {
	WorkspaceID string
	AuthCookie  string
}

type SecurityConfig struct {
	AllowedUsers               []string
	AllowedChannels            []string
	WorkspaceRoots             []string
	WorkspaceAutoFetch         bool
	PromptIncludeRepoInventory bool
}

type SessionConfig struct {
	MaxContextTokens    int    // context window token limit (default 200000)
	AutocompactBuffer   int    // reserved token headroom before auto-compact (default 13000)
	CompactModel        string // model used for compact summaries (empty = secondary model, then main model)
	MaxToolResultTokens int    // per-tool-result token cap (default 5000)
}

type ToolConfig struct {
	CommandTimeout         time.Duration
	AgentMaxSteps          int
	AgentMaxConcurrentRuns int
	GCloudPath             string
	GCPDefaultProject      string
	GCPDefaultNamespace    string
	GKEDefaultCluster      string
	GKEDefaultRegion       string
	KubectlPath            string
	K8sDefaultContext      string
	K8sDefaultCluster      string
	K8sDefaultNamespace    string
	TTSAPIKey              string
	TTSBaseURL             string
	TTSModel               string
	TTSAuto                bool
	TTSDefaultVoice        string
	TTSDefaultStyle        string
	NotionToken            string
	NotionDatabaseID       string
	NotionTitleProperty    string
	NotionVersion          string
	YouTrackURL            string
	YouTrackToken          string
	GitHubToken            string
	GitHubAPIBaseURL       string
	GitHubDefaultOwner     string
	GitHubDefaultRepo      string
	LuckinMCPURL           string
	LuckinMCPToken         string
	PlaywrightMCPURL       string
	PlaywrightMCPToken     string
	WebSearchProvider      string
	WebSearchGoogleKey     string
	WebSearchGoogleCX      string
	WebSearchSerpAPIKey    string
	WebSearchSerpAPIURL    string
	WebSearchSearXNGURL    string
	WebSearchBraveKey      string
	WebSearchBraveURL      string
}

type ObservingConfig struct {
	LogLevel                 string
	AdminToken               string
	AllowUnauthenticated     bool
	InputCostPerMTok         float64
	OutputCostPerMTok        float64
	CacheReadCostPerMTok     float64
	CacheCreationCostPerMTok float64
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
	llmProvider := inferLLMProvider()
	llmProtocol := providerProtocol(llmProvider)
	llmBaseURL := providerBaseURL(llmProvider)
	llmBaseURL = normalizeLLMBaseURL(llmBaseURL, llmProtocol)
	if llmProtocol == "" {
		llmProtocol = inferLLMProtocol(llmBaseURL)
	}
	anthropicFlavor := normalizeAnthropicFlavor(firstEnv("LLM_ANTHROPIC_FLAVOR", "ANTHROPIC_FLAVOR"))
	if anthropicFlavor == "" && llmProtocol == "anthropic" {
		anthropicFlavor = inferAnthropicFlavor(llmBaseURL)
	}
	llmModel := providerModel(llmProvider)
	llmAvailableModels := availableModels(llmProvider, llmModel)
	llmMultimodalModel := providerMultimodalModel(llmProvider)
	llmMultimodalModels := envCSVDefault("MULTIMODAL_MODELS", defaultMultimodalModels(llmMultimodalModel))
	llmThinking := providerThinking(llmProvider)
	if llmProvider == "mimo" && providerThinking(llmProvider) == "" {
		llmThinking = "disabled"
	}

	secondaryProvider := strings.TrimSpace(os.Getenv("SECONDARY_PROVIDER"))
	var secondaryBaseURL, secondaryAPIKey, secondaryModel, secondaryProtocol string
	if secondaryProvider != "" {
		secondaryBaseURL = providerBaseURL(secondaryProvider)
		secondaryAPIKey = providerAPIKey(secondaryProvider)
		secondaryProtocol = providerProtocol(secondaryProvider)
		secondaryModel = strings.TrimSpace(os.Getenv("SECONDARY_MODEL"))
		if secondaryModel == "" {
			secondaryModel = providerModel(secondaryProvider)
		}
	}

	cfg := Config{
		HTTP: HTTPConfig{
			Addr:                env("HTTP_ADDR", ":8080"),
			EventWorkers:        envInt("SLACK_EVENT_WORKERS", 8),
			EventQueueSize:      envInt("SLACK_EVENT_QUEUE_SIZE", 512),
			EventEnqueueTimeout: envDuration("SLACK_EVENT_ENQUEUE_TIMEOUT", 2*time.Second),
			EventTimeout:        envDuration("SLACK_EVENT_TIMEOUT", 15*time.Minute),
			EventInboxLease:     envDuration("SLACK_EVENT_INBOX_LEASE", 0),
			ShutdownTimeout:     envDuration("HTTP_SHUTDOWN_TIMEOUT", 30*time.Second),
		},
		Slack: SlackConfig{
			BotToken:      os.Getenv("SLACK_BOT_TOKEN"),
			SigningSecret: os.Getenv("SLACK_SIGNING_SECRET"),
			BotUserID:     os.Getenv("SLACK_BOT_USER_ID"),
		},
		LLM: LLMConfig{
			Provider:         llmProvider,
			BaseURL:          trimRightSlash(llmBaseURL),
			APIKey:           providerAPIKey(llmProvider),
			Model:            llmModel,
			AvailableModels:  llmAvailableModels,
			MultimodalModel:  llmMultimodalModel,
			MultimodalModels: llmMultimodalModels,
			TokenUsage: TokenUsageConfig{
				OpenCodeGo: OpenCodeGoTokenUsageConfig{
					WorkspaceID: os.Getenv("OPENCODE_GO_WORKSPACE_ID"),
					AuthCookie:  os.Getenv("OPENCODE_GO_AUTH_COOKIE"),
				},
			},
			Protocol:        llmProtocol,
			AnthropicFlavor: anthropicFlavor,
			Thinking:        llmThinking,
			Temperature:     providerTemperature(llmProvider),
			Timeout:         providerTimeout(llmProvider),

			SecondaryProvider: secondaryProvider,
			SecondaryBaseURL:  trimRightSlash(secondaryBaseURL),
			SecondaryAPIKey:   secondaryAPIKey,
			SecondaryModel:    secondaryModel,
			SecondaryProtocol: secondaryProtocol,
			DynamicStatus:     envBool("DYNAMIC_STATUS", true),
		},
		Security: SecurityConfig{
			AllowedUsers:               envCSV("ALLOWED_SLACK_USERS"),
			AllowedChannels:            envCSV("ALLOWED_SLACK_CHANNELS"),
			WorkspaceRoots:             normalizeRoots(envCSVDefault("WORKSPACE_ROOTS", []string{wd})),
			WorkspaceAutoFetch:         envBool("WORKSPACE_AUTO_FETCH", false),
			PromptIncludeRepoInventory: envBool("PROMPT_INCLUDE_REPO_INVENTORY", true),
		},
		Sessions: SessionConfig{
			MaxContextTokens:    envInt("SESSION_MAX_CONTEXT_TOKENS", 200000),
			AutocompactBuffer:   envInt("SESSION_AUTOCOMPACT_BUFFER", 13000),
			CompactModel:        env("SESSION_COMPACT_MODEL", ""),
			MaxToolResultTokens: envInt("SESSION_MAX_TOOL_RESULT_TOKENS", 8000),
		},
		Tools: ToolConfig{
			CommandTimeout:         envDuration("TOOL_COMMAND_TIMEOUT", 30*time.Second),
			AgentMaxSteps:          envInt("AGENT_MAX_STEPS", 256),
			AgentMaxConcurrentRuns: envInt("AGENT_MAX_CONCURRENT_RUNS", 16),
			GCloudPath:             env("GCLOUD_PATH", "gcloud"),
			GCPDefaultProject:      os.Getenv("GCP_PROJECT"),
			GCPDefaultNamespace:    env("GCP_NAMESPACE", ""),
			GKEDefaultCluster:      env("GKE_CLUSTER", ""),
			GKEDefaultRegion:       env("GKE_REGION", ""),
			KubectlPath:            env("KUBECTL_PATH", "kubectl"),
			K8sDefaultContext:      os.Getenv("K8S_DEFAULT_CONTEXT"),
			K8sDefaultCluster:      os.Getenv("K8S_DEFAULT_CLUSTER"),
			K8sDefaultNamespace:    env("K8S_DEFAULT_NAMESPACE", ""),
			TTSAPIKey:              firstEnv("TTS_API_KEY", "MIMO_API_KEY"),
			TTSBaseURL:             trimRightSlash(env("TTS_BASE_URL", "https://token-plan-cn.xiaomimimo.com/v1")),
			TTSModel:               env("TTS_MODEL", "mimo-v2.5-tts"),
			TTSAuto:                envBool("TTS_AUTO", false),
			TTSDefaultVoice:        env("TTS_DEFAULT_VOICE", "冰糖"),
			TTSDefaultStyle:        os.Getenv("TTS_DEFAULT_STYLE"),
			NotionToken:            os.Getenv("NOTION_TOKEN"),
			NotionDatabaseID:       os.Getenv("NOTION_DATABASE_ID"),
			NotionTitleProperty:    env("NOTION_TITLE_PROPERTY", "Name"),
			NotionVersion:          env("NOTION_VERSION", "2022-06-28"),
			YouTrackURL:            trimRightSlash(os.Getenv("YOUTRACK_URL")),
			YouTrackToken:          os.Getenv("YOUTRACK_TOKEN"),
			GitHubToken:            os.Getenv("GITHUB_TOKEN"),
			GitHubAPIBaseURL:       trimRightSlash(env("GITHUB_API_BASE_URL", "https://api.github.com")),
			GitHubDefaultOwner:     os.Getenv("GITHUB_DEFAULT_OWNER"),
			GitHubDefaultRepo:      os.Getenv("GITHUB_DEFAULT_REPO"),
			LuckinMCPURL:           trimRightSlash(env("LUCKIN_MCP_URL", "https://gwmcp.lkcoffee.com/order/user/mcp")),
			LuckinMCPToken:         os.Getenv("LUCKIN_MCP_TOKEN"),
			PlaywrightMCPURL:       trimRightSlash(os.Getenv("PLAYWRIGHT_MCP_URL")),
			PlaywrightMCPToken:     os.Getenv("PLAYWRIGHT_MCP_TOKEN"),
			WebSearchProvider:      env("WEB_SEARCH_PROVIDER", "duckduckgo"),
			WebSearchGoogleKey:     os.Getenv("WEB_SEARCH_GOOGLE_API_KEY"),
			WebSearchGoogleCX:      os.Getenv("WEB_SEARCH_GOOGLE_CX"),
			WebSearchSerpAPIKey:    os.Getenv("WEB_SEARCH_SERPAPI_KEY"),
			WebSearchSerpAPIURL:    trimRightSlash(env("WEB_SEARCH_SERPAPI_BASE_URL", "https://serpapi.com/search.json")),
			WebSearchSearXNGURL:    trimRightSlash(os.Getenv("WEB_SEARCH_SEARXNG_URL")),
			WebSearchBraveKey:      os.Getenv("WEB_SEARCH_BRAVE_API_KEY"),
			WebSearchBraveURL:      trimRightSlash(env("WEB_SEARCH_BRAVE_BASE_URL", "https://api.search.brave.com/res/v1/web/search")),
		},
		Observing: ObservingConfig{
			LogLevel:                 env("LOG_LEVEL", "info"),
			AdminToken:               os.Getenv("OBSERVABILITY_TOKEN"),
			AllowUnauthenticated:     envBool("OBSERVABILITY_ALLOW_UNAUTHENTICATED", false),
			InputCostPerMTok:         envFloat("LLM_INPUT_COST_PER_MTOK", -1),
			OutputCostPerMTok:        envFloat("LLM_OUTPUT_COST_PER_MTOK", -1),
			CacheReadCostPerMTok:     envFloat("LLM_CACHE_READ_COST_PER_MTOK", -1),
			CacheCreationCostPerMTok: envFloat("LLM_CACHE_CREATION_COST_PER_MTOK", -1),
		},
		Storage: StorageConfig{
			PostgresDSN: firstNonEmpty(os.Getenv("POSTGRES_DSN"), os.Getenv("REMINDER_POSTGRES_DSN")),
			RedisURL:    firstNonEmpty(os.Getenv("REDIS_URL"), os.Getenv("REDIS_DSN")),
		},
	}

	if cfg.Slack.SigningSecret == "" {
		return cfg, fmt.Errorf("SLACK_SIGNING_SECRET is required")
	}
	if cfg.Slack.BotToken == "" {
		return cfg, fmt.Errorf("SLACK_BOT_TOKEN is required")
	}
	if strings.Contains(cfg.LLM.BaseURL, "api.kimi.com/coding") {
		return cfg, fmt.Errorf("the Kimi coding endpoint is not supported directly; use LLM_PROVIDER=cliproxyapi and connect to a locally authenticated CLIProxyAPI instance, or configure KIMI_BASE_URL with Kimi's documented API endpoint")
	}
	if cfg.LLM.APIKey == "" {
		return cfg, fmt.Errorf("%s API key is required", strings.ToUpper(cfg.LLM.Provider))
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
	if cfg.Storage.PostgresDSN == "" {
		return cfg, fmt.Errorf("POSTGRES_DSN is required for durable session, event, run, and reminder storage")
	}
	if cfg.Storage.RedisURL == "" {
		return cfg, fmt.Errorf("REDIS_URL is required for cross-instance caching and event pub/sub")
	}
	if cfg.HTTP.EventInboxLease <= 0 {
		cfg.HTTP.EventInboxLease = cfg.HTTP.EventTimeout + time.Minute
	}
	if cfg.HTTP.EventWorkers <= 0 || cfg.HTTP.EventQueueSize <= 0 || cfg.HTTP.EventEnqueueTimeout <= 0 || cfg.HTTP.EventTimeout <= 0 || cfg.HTTP.EventInboxLease <= 0 || cfg.HTTP.ShutdownTimeout <= 0 || cfg.Tools.AgentMaxConcurrentRuns <= 0 {
		return cfg, fmt.Errorf("SLACK_EVENT_WORKERS, SLACK_EVENT_QUEUE_SIZE, SLACK_EVENT_ENQUEUE_TIMEOUT, SLACK_EVENT_TIMEOUT, SLACK_EVENT_INBOX_LEASE, HTTP_SHUTDOWN_TIMEOUT, and AGENT_MAX_CONCURRENT_RUNS must be positive")
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

func inferLLMProvider() string {
	provider := inferLLMProviderFrom(func(key string) string { return os.Getenv(key) })
	if provider == "" {
		return "mimo"
	}
	return provider
}

func inferLLMProviderFrom(get func(string) string) string {
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(get("LLM_PROVIDER"), get("LLM_VENDOR"))))
	if provider != "" {
		return provider
	}
	switch {
	case firstNonEmpty(get("LONGCAT_API_KEY"), get("LONGCAT_BASE_URL"), get("LONGCAT_MODEL"), get("LONGCAT_PROTOCOL")) != "":
		return "longcat"
	case firstNonEmpty(get("MIMO_API_KEY"), get("MIMO_BASE_URL"), get("MIMO_MODEL"), get("MIMO_PROTOCOL")) != "":
		return "mimo"
	case firstNonEmpty(get("ANTHROPIC_API_KEY"), get("ANTHROPIC_AUTH_TOKEN"), get("ANTHROPIC_BASE_URL"), get("ANTHROPIC_MODEL"), get("ANTHROPIC_PROTOCOL")) != "":
		return "anthropic"
	case firstNonEmpty(get("CLIPROXYAPI_API_KEY"), get("CLIPROXYAPI_BASE_URL"), get("CLIPROXYAPI_MODEL")) != "":
		return "cliproxyapi"
	case firstNonEmpty(get("KIMI_API_KEY"), get("KIMI_BASE_URL"), get("KIMI_MODEL"), get("KIMI_PROTOCOL")) != "":
		return "kimi"
	case firstNonEmpty(get("MOONSHOT_API_KEY"), get("MOONSHOT_BASE_URL"), get("MOONSHOT_MODEL")) != "":
		return "moonshot"
	case firstNonEmpty(get("OPENCODE_ZEN_API_KEY"), get("OPENCODE_ZEN_BASE_URL"), get("OPENCODE_ZEN_MODEL"), get("OPENCODE_ZEN_PROTOCOL")) != "":
		return "opencode-zen"
	case firstNonEmpty(get("OPENCODE_GO_API_KEY"), get("OPENCODE_GO_BASE_URL"), get("OPENCODE_GO_MODEL"), get("OPENCODE_GO_PROTOCOL")) != "":
		return "opencode-go"
	case firstNonEmpty(get("DEEPSEEK_API_KEY"), get("DEEPSEEK_BASE_URL"), get("DEEPSEEK_MODEL"), get("DEEPSEEK_PROTOCOL")) != "":
		return "deepseek"
	case firstNonEmpty(get("OPENAI_API_KEY"), get("OPENAI_BASE_URL"), get("OPENAI_MODEL")) != "":
		return "openai"
	}
	return ""
}

type providerDefaults struct {
	defaultProtocol string
	defaultBaseURL  string
	defaultModel    string
	apiKeyEnvs      []string
}

var providerTable = map[string]providerDefaults{
	"longcat":      {defaultProtocol: "anthropic", defaultBaseURL: "https://api.longcat.chat/anthropic", defaultModel: "LongCat-2.0", apiKeyEnvs: []string{"LONGCAT_API_KEY"}},
	"mimo":         {defaultProtocol: "anthropic", defaultBaseURL: "https://token-plan-cn.xiaomimimo.com/anthropic", defaultModel: "mimo-v2.5", apiKeyEnvs: []string{"MIMO_API_KEY"}},
	"anthropic":    {defaultProtocol: "", defaultBaseURL: "https://api.anthropic.com", defaultModel: "claude-sonnet-4-5-20250929", apiKeyEnvs: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}},
	"kimi":         {defaultProtocol: "", defaultBaseURL: "https://api.moonshot.ai/v1", defaultModel: "kimi-k2.6", apiKeyEnvs: []string{"KIMI_API_KEY"}},
	"cliproxyapi":  {defaultProtocol: "openai", defaultBaseURL: "http://127.0.0.1:8317/v1", defaultModel: "kimi/kimi-k2.7-code", apiKeyEnvs: []string{"CLIPROXYAPI_API_KEY"}},
	"moonshot":     {defaultProtocol: "", defaultBaseURL: "https://api.moonshot.ai/v1", defaultModel: "kimi-k2.6", apiKeyEnvs: []string{"MOONSHOT_API_KEY"}},
	"opencode-go":  {defaultProtocol: "openai", defaultBaseURL: "https://opencode.ai/zen/go/v1", defaultModel: "glm-5.2", apiKeyEnvs: []string{"OPENCODE_GO_API_KEY"}},
	"opencode-zen": {defaultProtocol: "openai", defaultBaseURL: "https://opencode.ai/zen/v1", defaultModel: "mimo-v2.5-free", apiKeyEnvs: []string{"OPENCODE_ZEN_API_KEY"}},
	"deepseek":     {defaultProtocol: "openai", defaultBaseURL: "https://api.deepseek.com", defaultModel: "deepseek-v4-flash", apiKeyEnvs: []string{"DEEPSEEK_API_KEY"}},
}

func providerProtocol(provider string) string {
	prefix := providerEnvPrefix(provider)
	protocol := normalizeLLMProtocol(firstEnv(prefix+"_PROTOCOL", "LLM_PROTOCOL"))
	if protocol != "" {
		return protocol
	}
	if defaults, ok := providerTable[provider]; ok && defaults.defaultProtocol != "" {
		return defaults.defaultProtocol
	}
	return normalizeLLMProtocol(firstEnv("LLM_PROTOCOL"))
}

func providerBaseURL(provider string) string {
	prefix := providerEnvPrefix(provider)
	if defaults, ok := providerTable[provider]; ok {
		return env(prefix+"_BASE_URL", defaults.defaultBaseURL)
	}
	return env("OPENAI_BASE_URL", "https://api.openai.com/v1")
}

func providerModel(provider string) string {
	prefix := providerEnvPrefix(provider)
	if provider == "kimi" && strings.Contains(providerBaseURL("kimi"), "api.kimi.com/coding") {
		return env("KIMI_MODEL", "kimi-for-coding")
	}
	if defaults, ok := providerTable[provider]; ok {
		return env(prefix+"_MODEL", defaults.defaultModel)
	}
	return env("OPENAI_MODEL", "gpt-4o-mini")
}

func providerMultimodalModel(provider string) string {
	if model := firstEnv("MODEL_ROUTING_MULTIMODAL_MODEL", "MULTIMODAL_MODEL"); model != "" {
		return model
	}
	return ""
}

func defaultMultimodalModels(model string) []string {
	if model == "" {
		return nil
	}
	return []string{model}
}

func availableModels(provider, defaultModel string) []string {
	raw := strings.TrimSpace(os.Getenv(providerEnvPrefix(provider) + "_AVAILABLE_MODELS"))
	if raw == "" {
		if provider == "opencode-go" {
			return ensureDefaultModel(defaultModel, opencodeGoModels())
		}
		if provider == "opencode-zen" {
			return ensureDefaultModel(defaultModel, opencodeZenModels())
		}
		if provider == "deepseek" {
			return ensureDefaultModel(defaultModel, deepSeekModels())
		}
		return []string{defaultModel}
	}
	seen := map[string]bool{}
	var models []string
	for _, m := range strings.Split(raw, ",") {
		m = strings.TrimSpace(m)
		if m != "" && !seen[m] {
			seen[m] = true
			models = append(models, m)
		}
	}
	if len(models) == 0 {
		return []string{defaultModel}
	}
	return ensureDefaultModel(defaultModel, models)
}

func ensureDefaultModel(defaultModel string, models []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(models)+1)
	for _, model := range models {
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		out = append(out, model)
	}
	if defaultModel != "" && !seen[defaultModel] {
		out = append([]string{defaultModel}, out...)
	}
	return out
}

func opencodeGoModels() []string {
	return []string{
		"glm-5.2",
		"glm-5.1",
		"kimi-k2.7-code",
		"kimi-k2.6",
		"mimo-v2.5",
		"mimo-v2.5-pro",
		"minimax-m3",
		"minimax-m2.7",
		"minimax-m2.5",
		"qwen3.7-max",
		"qwen3.7-plus",
		"qwen3.6-plus",
		"deepseek-v4-pro",
		"deepseek-v4-flash",
	}
}

func opencodeZenModels() []string {
	return []string{
		"mimo-v2.5-free",
		"minimax-m3-free",
		"nemotron-3-ultra-free",
		"north-mini-code-free",
	}
}

func deepSeekModels() []string {
	return []string{
		"deepseek-v4-flash",
		"deepseek-v4-pro",
	}
}

func providerAPIKey(provider string) string {
	if defaults, ok := providerTable[provider]; ok {
		return firstEnv(defaults.apiKeyEnvs...)
	}
	return firstEnv("OPENAI_API_KEY")
}

func providerThinking(provider string) string {
	switch provider {
	case "mimo":
		return firstEnv("MIMO_THINKING")
	case "kimi", "moonshot":
		return firstEnv("KIMI_THINKING")
	case "cliproxyapi":
		return firstEnv("CLIPROXYAPI_THINKING")
	case "deepseek":
		return firstEnv("DEEPSEEK_THINKING")
	default:
		return ""
	}
}

func providerTemperature(provider string) float64 {
	prefix := providerEnvPrefix(provider)
	if provider == "kimi" || provider == "moonshot" {
		return envFloatAliases(0, "KIMI_TEMPERATURE")
	}
	fallbackKeys := []string{prefix + "_TEMPERATURE"}
	if provider == "openai" || provider == "" {
		fallbackKeys = append(fallbackKeys, "ANTHROPIC_TEMPERATURE")
	}
	return envFloatAliases(0, fallbackKeys...)
}

func providerTimeout(provider string) time.Duration {
	prefix := providerEnvPrefix(provider)
	if provider == "kimi" || provider == "moonshot" {
		return envDurationAliases(120*time.Second, "KIMI_TIMEOUT", "API_TIMEOUT_MS")
	}
	return envDurationAliases(120*time.Second, prefix+"_TIMEOUT", "API_TIMEOUT_MS")
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
	if strings.Contains(trimRightSlash(raw), "xiaomimimo.com/anthropic") {
		return "claude-code"
	}
	return "official"
}

func providerEnvPrefix(provider string) string {
	switch provider {
	case "cliproxyapi":
		return "CLIPROXYAPI"
	case "opencode-go":
		return "OPENCODE_GO"
	case "opencode-zen":
		return "OPENCODE_ZEN"
	default:
		return strings.ToUpper(provider)
	}
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
	if shell.Provider != dotenv.Provider {
		conflicts = append(conflicts, "LLM_PROVIDER")
	}
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
	Provider        string
	Protocol        string
	AnthropicFlavor string
	BaseURL         string
	Model           string
	APIKey          string
}

func (s llmEnvSnapshot) configured() bool {
	return s.Provider != "" || s.Protocol != "" || s.AnthropicFlavor != "" || s.BaseURL != "" || s.Model != "" || s.APIKey != ""
}

func providerSnapshot(get func(string) string) llmEnvSnapshot {
	provider := inferLLMProviderFrom(get)
	if provider == "" {
		return llmEnvSnapshot{}
	}
	prefix := providerEnvPrefix(provider)
	protocol := normalizeLLMProtocol(firstNonEmpty(get(prefix+"_PROTOCOL"), get("LLM_PROTOCOL")))
	if protocol == "" && provider == "mimo" {
		protocol = "anthropic"
	}
	if protocol == "" && (provider == "opencode-go" || provider == "opencode-zen" || provider == "deepseek") {
		protocol = "openai"
	}
	anthropicFlavor := normalizeAnthropicFlavor(firstNonEmpty(
		get("LLM_ANTHROPIC_FLAVOR"),
		get("ANTHROPIC_FLAVOR"),
	))
	baseURL := firstNonEmpty(get(prefix + "_BASE_URL"))
	baseURL = normalizeLLMBaseURL(baseURL, protocol)
	if protocol == "" && baseURL != "" {
		protocol = inferLLMProtocol(baseURL)
	}
	if anthropicFlavor == "" && protocol == "anthropic" {
		anthropicFlavor = inferAnthropicFlavor(baseURL)
	}
	snapshot := llmEnvSnapshot{
		Provider:        provider,
		Protocol:        protocol,
		AnthropicFlavor: anthropicFlavor,
		BaseURL:         trimRightSlash(baseURL),
		Model:           firstNonEmpty(get(prefix + "_MODEL")),
		APIKey:          providerSnapshotAPIKey(provider, get),
	}
	return snapshot
}

func providerSnapshotAPIKey(provider string, get func(string) string) string {
	switch provider {
	case "anthropic":
		return firstNonEmpty(get("ANTHROPIC_API_KEY"), get("ANTHROPIC_AUTH_TOKEN"))
	default:
		return firstNonEmpty(get(providerEnvPrefix(provider) + "_API_KEY"))
	}
}

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
	HTTP         HTTPConfig
	Web          WebConfig
	Slack        SlackConfig
	LLM          LLMConfig
	Security     SecurityConfig
	Sessions     SessionConfig
	Tools        ToolConfig
	Integrations IntegrationConfig
	Connections  ConnectionsConfig
	Observing    ObservingConfig
	Storage      StorageConfig
}

// WebConfig controls the browser surface hosted by the worker. Slack is the
// first identity provider, but browser sessions and conversations remain
// independent from Slack messages, preferences, and user connections.
type WebConfig struct {
	Enabled       bool
	PublicBaseURL string
	UpstreamURL   string
	SessionSecret string
	SessionTTL    time.Duration
	SiteName  string
	StaticDir string
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
	EventMaxAttempts    int
	EventRetryBase      time.Duration
	EventRetryMax       time.Duration
	ShutdownTimeout     time.Duration
}

type SlackConfig struct {
	BotToken        string
	SigningSecret   string
	BotUserID       string
	DefaultLocale   string
	AttributionName string
	ReplyFooter     string
}

type LLMConfig struct {
	Provider         string
	BaseURL          string
	APIKey           string
	Model            string
	MaxOutputTokens  int
	MultimodalModel  string
	MultimodalModels []string
	ResponsesModels  []string
	Protocol         string
	AnthropicFlavor  string
	Thinking         string
	Temperature      *float64
	Timeout          time.Duration
	Resilience       ResilienceConfig

	// Secondary model is the preferred Explorer model and the primary agent's
	// fallback. Compact summaries use it when no explicit compact model exists.
	SecondaryProvider string
	SecondaryBaseURL  string
	SecondaryAPIKey   string
	SecondaryModel    string
	SecondaryProtocol string
}

type ResilienceConfig struct {
	MaxAttempts      int
	RetryBaseDelay   time.Duration
	MinAttemptBudget time.Duration
	FailureThreshold int
	CircuitCooldown  time.Duration
}

type SecurityConfig struct {
	AllowedUsers       []string
	AllowedChannels    []string
	WorkspaceRoots     []string
	WorkspaceAutoFetch bool
}

type SessionConfig struct {
	MaxContextTokens    int    // context window token limit (default 200000)
	AutocompactBuffer   int    // reserved token headroom before auto-compact (default 13000)
	CompactModel        string // model used for compact summaries (empty = secondary model, then main model)
	MaxToolResultTokens int    // per-tool-result token cap (default 8000)
}

type ToolConfig struct {
	CommandTimeout       time.Duration
	AgentMaxSteps        int
	AgentExploreMaxSteps int
	AgentExploreTimeout  time.Duration
	AllowedWriteTools    []string
}

type IntegrationConfig struct {
	GCP        GCPConfig
	GitHub     GitHubConfig
	K8s        K8sConfig
	Luckin     LuckinConfig
	ClickStack ClickStackConfig
	Notion     NotionConfig
	TTS        TTSConfig
	WebSearch  WebSearchConfig
	YouTrack   YouTrackConfig
}

type GCPConfig struct {
	GCloudPath       string
	DefaultProject   string
	DefaultNamespace string
	DefaultCluster   string
	DefaultRegion    string
}

type GitHubConfig struct {
	Token        string
	APIBaseURL   string
	DefaultOwner string
	DefaultRepo  string
}

type K8sConfig struct {
	KubectlPath      string
	DefaultContext   string
	DefaultCluster   string
	DefaultNamespace string
}

type LuckinConfig struct {
	MCPURL   string
	MCPToken string
}

type ClickStackConfig struct {
	MCPURL    string
	ServiceID string
	TeamID    string
}

func (c ClickStackConfig) Configured() bool {
	return strings.TrimSpace(c.ServiceID) != "" || customClickStackDeployURL(c.MCPURL)
}

func customClickStackDeployURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw != "" && raw != defaultClickStackMCPURL
}

func (c ClickStackConfig) Enabled() bool {
	return c.Configured()
}

type NotionConfig struct {
	MCPURL string
}

func (c NotionConfig) Enabled() bool {
	return true
}

type TTSConfig struct {
	APIKey       string
	BaseURL      string
	Model        string
	DefaultVoice string
	DefaultStyle string
}

type WebSearchConfig struct {
	Provider   string
	GoogleKey  string
	GoogleCX   string
	SerpAPIKey string
	SerpAPIURL string
	SearXNGURL string
	BraveKey   string
	BraveURL   string
}

type YouTrackConfig struct {
	URL   string
	Token string
}

type ConnectionsConfig struct {
	PublicBaseURL        string
	EncryptionKey        string
	SlackClientID        string
	SlackClientSecret    string
	GitHubClientID       string
	GitHubClientSecret   string
	GCPOAuthClientID     string
	GCPOAuthClientSecret string
	LocalOAuthPort       int
}

func (c ConnectionsConfig) GCPOAuthEnabled() bool {
	return strings.TrimSpace(c.GCPOAuthClientID) != "" && strings.TrimSpace(c.GCPOAuthClientSecret) != ""
}

func (c ConnectionsConfig) SlackOAuthEnabled() bool {
	return strings.TrimSpace(c.SlackClientID) != "" && strings.TrimSpace(c.SlackClientSecret) != ""
}

func (c ConnectionsConfig) GitHubOAuthEnabled() bool {
	return strings.TrimSpace(c.GitHubClientID) != "" && strings.TrimSpace(c.GitHubClientSecret) != ""
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

type RuntimeProfile string

const (
	ProfileGateway       RuntimeProfile = "gateway"
	ProfileSlackWorker   RuntimeProfile = "slack-worker"
	ProfileObservability RuntimeProfile = "observability"
	ProfileCLI           RuntimeProfile = "cli"
)

const defaultClickStackMCPURL = "https://mcp.clickhouse.cloud/clickstack"
const defaultNotionMCPURL = "https://mcp.notion.com/mcp"

func Load() (Config, error) {
	return LoadFor(ProfileSlackWorker)
}

func LoadFor(profile RuntimeProfile) (Config, error) {
	cfg, err := loadRaw(profile)
	if err != nil {
		return cfg, err
	}
	return validateForProfile(cfg, profile)
}

func loadRaw(profile RuntimeProfile) (Config, error) {
	dotenvPath := dotenvPath(profile)
	dotenvValues, err := readDotEnv(dotenvPath)
	if err != nil {
		return Config{}, err
	}
	applyDotEnv(dotenvValues)
	if err := validateTypedEnvironment(); err != nil {
		return Config{}, err
	}
	wd, _ := os.Getwd()
	llmProvider := strings.ToLower(env("LLM_PROVIDER", "mimo"))
	llmProtocol := providerProtocol(llmProvider)
	llmBaseURL := providerBaseURL(llmProvider)
	anthropicFlavor := providerAnthropicFlavor(llmProvider)
	llmModel := providerModel(llmProvider)
	llmMultimodalModel := multimodalModel()
	llmMultimodalModels := envCSVDefault("MULTIMODAL_MODELS", defaultMultimodalModels(llmMultimodalModel))
	llmThinking := providerThinking(llmProvider)
	if llmProvider == "mimo" && llmThinking == "" {
		llmThinking = "disabled"
	}

	secondaryProvider := strings.ToLower(strings.TrimSpace(os.Getenv("SECONDARY_PROVIDER")))
	if err := validateProviderTypedEnvironment(llmProvider, secondaryProvider); err != nil {
		return Config{}, err
	}
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
			EventMaxAttempts:    envInt("SLACK_EVENT_MAX_ATTEMPTS", 5),
			EventRetryBase:      envDuration("SLACK_EVENT_RETRY_BASE", time.Second),
			EventRetryMax:       envDuration("SLACK_EVENT_RETRY_MAX", time.Minute),
			ShutdownTimeout:     envDuration("HTTP_SHUTDOWN_TIMEOUT", 30*time.Second),
		},
		Web: WebConfig{
			Enabled:       envBool("WEB_ENABLED", false),
			PublicBaseURL: trimRightSlash(env("WEB_PUBLIC_BASE_URL", os.Getenv("CONNECTIONS_PUBLIC_BASE_URL"))),
			UpstreamURL:   trimRightSlash(os.Getenv("WEB_UPSTREAM_URL")),
			SessionSecret: os.Getenv("WEB_SESSION_SECRET"),
			SessionTTL:    envDuration("WEB_SESSION_TTL", 7*24*time.Hour),
			SiteName:  env("WEB_SITE_NAME", "Kepler"),
			StaticDir: env("WEB_STATIC_DIR", ""),
		},
		Slack: SlackConfig{
			BotToken:        os.Getenv("SLACK_BOT_TOKEN"),
			SigningSecret:   os.Getenv("SLACK_SIGNING_SECRET"),
			BotUserID:       os.Getenv("SLACK_BOT_USER_ID"),
			DefaultLocale:   env("SLACK_DEFAULT_LOCALE", "en-US"),
			AttributionName: env("SLACK_ATTRIBUTION_NAME", env("ATTRIBUTION_NAME", "斗包")),
			ReplyFooter:     firstEnv("SLACK_REPLY_FOOTER", "REPLY_FOOTER"),
		},
		LLM: LLMConfig{
			Provider:         llmProvider,
			BaseURL:          trimRightSlash(llmBaseURL),
			APIKey:           providerAPIKey(llmProvider),
			Model:            llmModel,
			MaxOutputTokens:  envInt("LLM_MAX_OUTPUT_TOKENS", 0),
			MultimodalModel:  llmMultimodalModel,
			MultimodalModels: llmMultimodalModels,
			ResponsesModels:  providerResponsesModels(llmProvider),
			Protocol:         llmProtocol,
			AnthropicFlavor:  anthropicFlavor,
			Thinking:         llmThinking,
			Temperature:      providerTemperature(llmProvider),
			Timeout:          providerTimeout(llmProvider),
			Resilience:       ResilienceConfig{MaxAttempts: envInt("LLM_RESILIENCE_MAX_ATTEMPTS", 3), RetryBaseDelay: envDuration("LLM_RESILIENCE_RETRY_BASE", 500*time.Millisecond), MinAttemptBudget: envDuration("LLM_RESILIENCE_MIN_ATTEMPT_BUDGET", 45*time.Second), FailureThreshold: envInt("LLM_CIRCUIT_FAILURE_THRESHOLD", 3), CircuitCooldown: envDuration("LLM_CIRCUIT_COOLDOWN", 30*time.Second)},

			SecondaryProvider: secondaryProvider,
			SecondaryBaseURL:  trimRightSlash(secondaryBaseURL),
			SecondaryAPIKey:   secondaryAPIKey,
			SecondaryModel:    secondaryModel,
			SecondaryProtocol: secondaryProtocol,
		},
		Security: SecurityConfig{
			AllowedUsers:       envCSV("ALLOWED_SLACK_USERS"),
			AllowedChannels:    envCSV("ALLOWED_SLACK_CHANNELS"),
			WorkspaceRoots:     normalizeRoots(envCSVDefault("WORKSPACE_ROOTS", []string{wd})),
			WorkspaceAutoFetch: envBool("WORKSPACE_AUTO_FETCH", false),
		},
		Sessions: SessionConfig{
			MaxContextTokens:    envInt("SESSION_MAX_CONTEXT_TOKENS", 200000),
			AutocompactBuffer:   envInt("SESSION_AUTOCOMPACT_BUFFER", 13000),
			CompactModel:        env("SESSION_COMPACT_MODEL", ""),
			MaxToolResultTokens: envInt("SESSION_MAX_TOOL_RESULT_TOKENS", 8000),
		},
		Tools: ToolConfig{
			CommandTimeout:       envDuration("TOOL_COMMAND_TIMEOUT", 30*time.Second),
			AgentMaxSteps:        envInt("AGENT_MAX_STEPS", 256),
			AgentExploreMaxSteps: envInt("AGENT_EXPLORE_MAX_STEPS", 8),
			AgentExploreTimeout:  envDuration("AGENT_EXPLORE_TIMEOUT", 2*time.Minute),
			AllowedWriteTools: envCSVDefault("AGENT_ALLOWED_WRITE_TOOLS", []string{
				"luckin-cancel_order", "luckin-create_order", "reminder-create", "reminder-cancel", "slack-create_canvas", "slack-user_post_message", "tts-speak",
			}),
		},
		Integrations: loadIntegrations(),
		Connections:  loadConnections(),
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
			PostgresDSN: os.Getenv("POSTGRES_DSN"),
			RedisURL:    os.Getenv("REDIS_URL"),
		},
	}
	return cfg, nil
}

func loadIntegrations() IntegrationConfig {
	return IntegrationConfig{
		GCP: GCPConfig{
			GCloudPath:       env("GCLOUD_PATH", "gcloud"),
			DefaultProject:   os.Getenv("GCP_PROJECT"),
			DefaultNamespace: env("GCP_NAMESPACE", ""),
			DefaultCluster:   env("GKE_CLUSTER", ""),
			DefaultRegion:    env("GKE_REGION", ""),
		},
		K8s: K8sConfig{
			KubectlPath:      env("KUBECTL_PATH", "kubectl"),
			DefaultContext:   os.Getenv("K8S_DEFAULT_CONTEXT"),
			DefaultCluster:   os.Getenv("K8S_DEFAULT_CLUSTER"),
			DefaultNamespace: env("K8S_DEFAULT_NAMESPACE", ""),
		},
		TTS: TTSConfig{
			APIKey:       os.Getenv("TTS_API_KEY"),
			BaseURL:      trimRightSlash(env("TTS_BASE_URL", "https://token-plan-cn.xiaomimimo.com/v1")),
			Model:        env("TTS_MODEL", "mimo-v2.5-tts"),
			DefaultVoice: env("TTS_DEFAULT_VOICE", "冰糖"),
			DefaultStyle: os.Getenv("TTS_DEFAULT_STYLE"),
		},
		Notion: NotionConfig{
			MCPURL: trimRightSlash(env("NOTION_MCP_URL", defaultNotionMCPURL)),
		},
		YouTrack: YouTrackConfig{
			URL:   trimRightSlash(os.Getenv("YOUTRACK_URL")),
			Token: os.Getenv("YOUTRACK_TOKEN"),
		},
		GitHub: GitHubConfig{
			Token:        os.Getenv("GITHUB_TOKEN"),
			APIBaseURL:   trimRightSlash(env("GITHUB_API_BASE_URL", "https://api.github.com")),
			DefaultOwner: os.Getenv("GITHUB_DEFAULT_OWNER"),
			DefaultRepo:  os.Getenv("GITHUB_DEFAULT_REPO"),
		},
		Luckin: LuckinConfig{
			MCPURL:   trimRightSlash(env("LUCKIN_MCP_URL", "https://gwmcp.lkcoffee.com/order/user/mcp")),
			MCPToken: os.Getenv("LUCKIN_MCP_TOKEN"),
		},
		ClickStack: ClickStackConfig{
			MCPURL:    trimRightSlash(env("CLICKSTACK_MCP_URL", defaultClickStackMCPURL)),
			ServiceID: os.Getenv("CLICKSTACK_SERVICE_ID"),
			TeamID:    os.Getenv("CLICKSTACK_TEAM_ID"),
		},
		WebSearch: WebSearchConfig{
			Provider:   env("WEB_SEARCH_PROVIDER", "duckduckgo"),
			GoogleKey:  os.Getenv("WEB_SEARCH_GOOGLE_API_KEY"),
			GoogleCX:   os.Getenv("WEB_SEARCH_GOOGLE_CX"),
			SerpAPIKey: os.Getenv("WEB_SEARCH_SERPAPI_KEY"),
			SerpAPIURL: trimRightSlash(env("WEB_SEARCH_SERPAPI_BASE_URL", "https://serpapi.com/search.json")),
			SearXNGURL: trimRightSlash(os.Getenv("WEB_SEARCH_SEARXNG_URL")),
			BraveKey:   os.Getenv("WEB_SEARCH_BRAVE_API_KEY"),
			BraveURL:   trimRightSlash(env("WEB_SEARCH_BRAVE_BASE_URL", "https://api.search.brave.com/res/v1/web/search")),
		},
	}
}

func loadConnections() ConnectionsConfig {
	port := envInt("CONNECTIONS_LOCAL_OAUTH_PORT", 8765)
	clientID := strings.TrimSpace(os.Getenv("SLACK_OAUTH_CLIENT_ID"))
	if clientID == "" {
		clientID = strings.TrimSpace(os.Getenv("SLACK_CLIENT_ID"))
	}
	clientSecret := strings.TrimSpace(os.Getenv("SLACK_OAUTH_CLIENT_SECRET"))
	if clientSecret == "" {
		clientSecret = strings.TrimSpace(os.Getenv("SLACK_CLIENT_SECRET"))
	}
	return ConnectionsConfig{
		PublicBaseURL:        trimRightSlash(os.Getenv("CONNECTIONS_PUBLIC_BASE_URL")),
		EncryptionKey:        os.Getenv("CONNECTIONS_ENCRYPTION_KEY"),
		SlackClientID:        clientID,
		SlackClientSecret:    clientSecret,
		GitHubClientID:       os.Getenv("GITHUB_OAUTH_CLIENT_ID"),
		GitHubClientSecret:   os.Getenv("GITHUB_OAUTH_CLIENT_SECRET"),
		GCPOAuthClientID:     os.Getenv("GCP_OAUTH_CLIENT_ID"),
		GCPOAuthClientSecret: os.Getenv("GCP_OAUTH_CLIENT_SECRET"),
		LocalOAuthPort:       port,
	}
}

func dotenvPath(profile RuntimeProfile) string {
	if path := strings.TrimSpace(os.Getenv("KEPLER_AGENT_ENV_FILE")); path != "" {
		return path
	}
	switch profile {
	case ProfileGateway:
		return "gateway/.env"
	case ProfileSlackWorker:
		return "worker/.env"
	case ProfileObservability:
		return "observability/.env"
	case ProfileCLI:
		return "worker/.env"
	case "":
		return "worker/.env"
	default:
		return "worker/.env"
	}
}

func validateForProfile(cfg Config, profile RuntimeProfile) (Config, error) {
	if profile == ProfileCLI {
		return cfg, nil
	}
	if cfg.HTTP.EventInboxLease <= 0 {
		cfg.HTTP.EventInboxLease = cfg.HTTP.EventTimeout + time.Minute
	}
	if cfg.HTTP.EventInboxLease <= cfg.HTTP.EventTimeout {
		return cfg, fmt.Errorf("SLACK_EVENT_INBOX_LEASE must be greater than SLACK_EVENT_TIMEOUT")
	}
	if cfg.HTTP.EventWorkers <= 0 || cfg.HTTP.EventQueueSize <= 0 || cfg.HTTP.EventEnqueueTimeout <= 0 || cfg.HTTP.EventTimeout <= 0 || cfg.HTTP.EventMaxAttempts <= 0 || cfg.HTTP.EventRetryBase <= 0 || cfg.HTTP.EventRetryMax < cfg.HTTP.EventRetryBase || cfg.HTTP.ShutdownTimeout <= 0 {
		return cfg, fmt.Errorf("event worker, queue, timeout, retry, and shutdown settings must be positive and internally consistent")
	}
	seenWriteTools := make(map[string]bool, len(cfg.Tools.AllowedWriteTools))
	for _, name := range cfg.Tools.AllowedWriteTools {
		if !validToolName(name) {
			return cfg, fmt.Errorf("AGENT_ALLOWED_WRITE_TOOLS contains invalid tool name %q", name)
		}
		if seenWriteTools[name] {
			return cfg, fmt.Errorf("AGENT_ALLOWED_WRITE_TOOLS contains duplicate tool name %q", name)
		}
		seenWriteTools[name] = true
	}
	if cfg.Storage.PostgresDSN == "" {
		return cfg, fmt.Errorf("POSTGRES_DSN is required for durable session, event, run, and reminder storage")
	}
	if cfg.Storage.RedisURL == "" {
		return cfg, fmt.Errorf("REDIS_URL is required for cross-instance caching and event pub/sub")
	}

	switch profile {
	case ProfileGateway:
		if cfg.Slack.SigningSecret == "" {
			return cfg, fmt.Errorf("SLACK_SIGNING_SECRET is required")
		}
		if cfg.Web.Enabled && cfg.Web.UpstreamURL == "" {
			return cfg, fmt.Errorf("WEB_UPSTREAM_URL is required on the gateway when WEB_ENABLED=true")
		}
		return cfg, nil
	case ProfileObservability:
		return cfg, nil
	case ProfileCLI:
		return cfg, nil
	case ProfileSlackWorker, "":
		cfg, err := validateAgentRuntime(cfg)
		if err != nil {
			return cfg, err
		}
		if err := validateWeb(cfg); err != nil {
			return cfg, err
		}
		return cfg, nil
	default:
		return cfg, fmt.Errorf("unknown runtime profile %q", profile)
	}
}

func validateWeb(cfg Config) error {
	if !cfg.Web.Enabled {
		return nil
	}
	if cfg.Web.PublicBaseURL == "" {
		return fmt.Errorf("WEB_PUBLIC_BASE_URL is required when WEB_ENABLED=true")
	}
	if len(cfg.Web.SessionSecret) < 32 {
		return fmt.Errorf("WEB_SESSION_SECRET must contain at least 32 characters")
	}
	if cfg.Web.SessionTTL <= 0 {
		return fmt.Errorf("WEB_SESSION_TTL must be positive")
	}
	if !cfg.Connections.SlackOAuthEnabled() {
		return fmt.Errorf("SLACK_OAUTH_CLIENT_ID and SLACK_OAUTH_CLIENT_SECRET are required when WEB_ENABLED=true")
	}
	return nil
}

func validToolName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func validateAgentRuntime(cfg Config) (Config, error) {
	if cfg.Slack.SigningSecret == "" {
		return cfg, fmt.Errorf("SLACK_SIGNING_SECRET is required")
	}
	if cfg.Slack.BotToken == "" {
		return cfg, fmt.Errorf("SLACK_BOT_TOKEN is required")
	}
	var err error
	cfg, err = validateModelRuntime(cfg)
	if err != nil {
		return cfg, err
	}
	if len(cfg.Security.AllowedUsers) == 0 {
		return cfg, fmt.Errorf("ALLOWED_SLACK_USERS is required")
	}
	return cfg, nil
}

func validateModelRuntime(cfg Config) (Config, error) {
	if cfg.LLM.APIKey == "" {
		return cfg, fmt.Errorf("%s API key is required", strings.ToUpper(cfg.LLM.Provider))
	}
	if cfg.LLM.Protocol != "openai" && cfg.LLM.Protocol != "anthropic" && cfg.LLM.Protocol != "responses" {
		return cfg, fmt.Errorf("model protocol must be openai, anthropic, or responses")
	}
	if cfg.LLM.AnthropicFlavor != "" && cfg.LLM.AnthropicFlavor != "official" && cfg.LLM.AnthropicFlavor != "claude-code" {
		return cfg, fmt.Errorf("LLM_ANTHROPIC_FLAVOR must be official or claude-code")
	}
	if cfg.LLM.Timeout <= 0 {
		return cfg, fmt.Errorf("%s_TIMEOUT must be positive", providerEnvPrefix(cfg.LLM.Provider))
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

type providerDefaults struct {
	protocol        string
	baseURL         string
	model           string
	anthropicFlavor string
	apiKeyEnvs      []string
}

var providerTable = map[string]providerDefaults{
	"longcat":      {protocol: "anthropic", baseURL: "https://api.longcat.chat/anthropic", model: "LongCat-2.0", anthropicFlavor: "official", apiKeyEnvs: []string{"LONGCAT_API_KEY"}},
	"mimo":         {protocol: "anthropic", baseURL: "https://token-plan-cn.xiaomimimo.com/anthropic", model: "mimo-v2.5", anthropicFlavor: "claude-code", apiKeyEnvs: []string{"MIMO_API_KEY"}},
	"anthropic":    {protocol: "anthropic", baseURL: "https://api.anthropic.com", model: "claude-sonnet-4-5-20250929", anthropicFlavor: "official", apiKeyEnvs: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}},
	"openai":       {protocol: "openai", baseURL: "https://api.openai.com/v1", model: "gpt-4o-mini", apiKeyEnvs: []string{"OPENAI_API_KEY"}},
	"kimi":         {protocol: "openai", baseURL: "https://api.moonshot.ai/v1", model: "kimi-k2.6", apiKeyEnvs: []string{"KIMI_API_KEY"}},
	"cliproxyapi":  {protocol: "openai", baseURL: "http://127.0.0.1:8317/v1", model: "kimi/kimi-k2.7-code", apiKeyEnvs: []string{"CLIPROXYAPI_API_KEY"}},
	"moonshot":     {protocol: "openai", baseURL: "https://api.moonshot.ai/v1", model: "kimi-k2.6", apiKeyEnvs: []string{"MOONSHOT_API_KEY"}},
	"opencode-go":  {protocol: "openai", baseURL: "https://opencode.ai/zen/go/v1", model: "glm-5.2", apiKeyEnvs: []string{"OPENCODE_GO_API_KEY"}},
	"opencode-zen": {protocol: "openai", baseURL: "https://opencode.ai/zen/v1", model: "mimo-v2.5-free", apiKeyEnvs: []string{"OPENCODE_ZEN_API_KEY"}},
	"deepseek":     {protocol: "openai", baseURL: "https://api.deepseek.com", model: "deepseek-v4-flash", apiKeyEnvs: []string{"DEEPSEEK_API_KEY"}},
}

func providerProtocol(provider string) string {
	prefix := providerEnvPrefix(provider)
	protocol := strings.ToLower(firstEnv(prefix+"_PROTOCOL", "LLM_PROTOCOL"))
	if protocol != "" {
		return protocol
	}
	if defaults, ok := providerTable[provider]; ok {
		return defaults.protocol
	}
	return ""
}

func providerBaseURL(provider string) string {
	prefix := providerEnvPrefix(provider)
	if defaults, ok := providerTable[provider]; ok {
		return env(prefix+"_BASE_URL", defaults.baseURL)
	}
	return strings.TrimSpace(os.Getenv(prefix + "_BASE_URL"))
}

func providerModel(provider string) string {
	prefix := providerEnvPrefix(provider)
	if defaults, ok := providerTable[provider]; ok {
		return env(prefix+"_MODEL", defaults.model)
	}
	return strings.TrimSpace(os.Getenv(prefix + "_MODEL"))
}

func providerResponsesModels(provider string) []string {
	prefix := providerEnvPrefix(provider)
	return envCSV(prefix + "_RESPONSES_MODELS")
}

func multimodalModel() string {
	return strings.TrimSpace(os.Getenv("MODEL_ROUTING_MULTIMODAL_MODEL"))
}

func defaultMultimodalModels(model string) []string {
	if model == "" {
		return nil
	}
	return []string{model}
}

func providerAPIKey(provider string) string {
	if defaults, ok := providerTable[provider]; ok {
		return firstEnv(defaults.apiKeyEnvs...)
	}
	return strings.TrimSpace(os.Getenv(providerEnvPrefix(provider) + "_API_KEY"))
}

func providerAnthropicFlavor(provider string) string {
	if flavor := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_ANTHROPIC_FLAVOR"))); flavor != "" {
		return flavor
	}
	return providerTable[provider].anthropicFlavor
}

func providerThinking(provider string) string {
	switch provider {
	case "mimo":
		return firstEnv("MIMO_THINKING")
	case "kimi", "moonshot":
		return firstEnv("KIMI_THINKING")
	case "cliproxyapi":
		return firstEnv("CLIPROXYAPI_THINKING")
	case "opencode-go":
		return firstEnv("OPENCODE_GO_THINKING")
	case "deepseek":
		return firstEnv("DEEPSEEK_THINKING")
	default:
		return ""
	}
}

func providerTemperature(provider string) *float64 {
	raw := strings.TrimSpace(os.Getenv(providerEnvPrefix(provider) + "_TEMPERATURE"))
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return &v
}

func providerTimeout(provider string) time.Duration {
	return envDuration(providerEnvPrefix(provider)+"_TIMEOUT", 120*time.Second)
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

func validateTypedEnvironment() error {
	integers := []string{"AGENT_MAX_STEPS", "AGENT_EXPLORE_MAX_STEPS", "LLM_MAX_OUTPUT_TOKENS", "LLM_RESILIENCE_MAX_ATTEMPTS", "LLM_CIRCUIT_FAILURE_THRESHOLD", "SESSION_AUTOCOMPACT_BUFFER", "SESSION_MAX_CONTEXT_TOKENS", "SESSION_MAX_TOOL_RESULT_TOKENS", "SLACK_EVENT_MAX_ATTEMPTS", "SLACK_EVENT_QUEUE_SIZE", "SLACK_EVENT_WORKERS"}
	for _, key := range integers {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			if _, err := strconv.Atoi(raw); err != nil {
				return fmt.Errorf("%s must be an integer: %w", key, err)
			}
		}
	}
	durations := []string{"HTTP_SHUTDOWN_TIMEOUT", "SLACK_EVENT_ENQUEUE_TIMEOUT", "SLACK_EVENT_INBOX_LEASE", "SLACK_EVENT_RETRY_BASE", "SLACK_EVENT_RETRY_MAX", "SLACK_EVENT_TIMEOUT", "TOOL_COMMAND_TIMEOUT", "AGENT_EXPLORE_TIMEOUT", "LLM_RESILIENCE_RETRY_BASE", "LLM_RESILIENCE_MIN_ATTEMPT_BUDGET", "LLM_CIRCUIT_COOLDOWN"}
	for _, key := range durations {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		if _, err := time.ParseDuration(raw); err != nil {
			if _, secondsErr := strconv.Atoi(raw); secondsErr != nil {
				return fmt.Errorf("%s must be a duration or integer seconds", key)
			}
		}
	}
	floats := []string{"LLM_CACHE_CREATION_COST_PER_MTOK", "LLM_CACHE_READ_COST_PER_MTOK", "LLM_INPUT_COST_PER_MTOK", "LLM_OUTPUT_COST_PER_MTOK"}
	for _, key := range floats {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			if _, err := strconv.ParseFloat(raw, 64); err != nil {
				return fmt.Errorf("%s must be numeric: %w", key, err)
			}
		}
	}
	booleans := []string{"OBSERVABILITY_ALLOW_UNAUTHENTICATED", "WORKSPACE_AUTO_FETCH"}
	for _, key := range booleans {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
		case "", "1", "true", "yes", "on", "0", "false", "no", "off":
		default:
			return fmt.Errorf("%s must be a boolean", key)
		}
	}
	return nil
}

func validateProviderTypedEnvironment(providers ...string) error {
	seen := make(map[string]bool, len(providers))
	for _, provider := range providers {
		provider = strings.TrimSpace(provider)
		if provider == "" {
			continue
		}
		prefix := providerEnvPrefix(provider)
		if seen[prefix] {
			continue
		}
		seen[prefix] = true
		if key := prefix + "_TEMPERATURE"; strings.TrimSpace(os.Getenv(key)) != "" {
			if _, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(key)), 64); err != nil {
				return fmt.Errorf("%s must be numeric: %w", key, err)
			}
		}
		if key := prefix + "_TIMEOUT"; strings.TrimSpace(os.Getenv(key)) != "" {
			raw := strings.TrimSpace(os.Getenv(key))
			if _, err := time.ParseDuration(raw); err != nil {
				if _, secondsErr := strconv.Atoi(raw); secondsErr != nil {
					return fmt.Errorf("%s must be a duration or integer seconds", key)
				}
			}
		}
	}
	return nil
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

func applyDotEnv(values map[string]string) {
	for key, value := range values {
		_ = os.Setenv(key, value)
	}
}

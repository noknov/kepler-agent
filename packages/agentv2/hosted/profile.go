package hosted

import (
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/providers/hosted"
	agentruntime "github.com/noknov/slack-copilot-agent/packages/agentv2/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/transcript"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/hostedtools"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	"github.com/noknov/slack-copilot-agent/packages/reminder"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/slack"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

// Profile owns the complete hosted-agent composition. Product entrypoints
// depend on this profile instead of constructing a second agent runtime first.
type Profile struct {
	Agent    Agent
	Prompt   safety.PromptPolicy
	Redactor safety.Redactor
	Tools    *tool.Catalog
	Rates    observability.CostRates
}

type ProfileDependencies struct {
	Slack      *slack.Client
	Reminders  reminder.Store
	Redis      *redisclient.Client
	UserPrefs  userprefs.Store
	Postgres   *pgxpool.Pool
	ToolSpills registry.ToolSpillStore
	Events     transcript.Sink
}

func NewProfile(cfg config.Config, deps ProfileDependencies) (Profile, error) {
	primary := buildModelClient(cfg.LLM.Provider, cfg.LLM.Protocol, cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Timeout, cfg.LLM.AnthropicFlavor)
	secondary, secondaryModel := secondaryModelClient(cfg)
	workspacePolicy := safety.WorkspacePolicy{Roots: cfg.Security.WorkspaceRoots}
	hostedTools := hostedtools.NewCatalog(cfg, deps.Slack, deps.Reminders, workspacePolicy, safety.NewCommandPolicy(), deps.Redis, deps.UserPrefs)
	catalog, err := AdaptRegistry(hostedTools)
	if err != nil {
		return Profile{}, fmt.Errorf("build hosted tool catalog: %w", err)
	}
	artifacts := PGArtifactStore{Store: deps.ToolSpills}
	if deps.ToolSpills != nil {
		if err := catalog.Register(ArtifactReadTool{Store: deps.ToolSpills}); err != nil {
			return Profile{}, err
		}
	}
	client := hostedmodel.Model{Client: primary}
	compactClient, compactModel := client, cfg.Sessions.CompactModel
	if secondary != nil {
		compactClient = hostedmodel.Model{Client: secondary}
		if compactModel == "" {
			compactModel = secondaryModel
		}
	}
	if compactModel == "" {
		compactModel = cfg.LLM.Model
	}
	runner, err := agentruntime.New(agentruntime.Config{
		Model: cfg.LLM.Model, ReasoningEffort: cfg.LLM.Thinking,
		MaxOutputTokens: cfg.LLM.MaxOutputTokens, MaxSteps: cfg.Tools.AgentMaxSteps,
		Context:     agentruntime.ContextConfig{MaxTokens: cfg.Sessions.MaxContextTokens, ReserveTokens: cfg.Sessions.AutocompactBuffer},
		ToolResults: agentruntime.ToolResultConfig{MaxInlineBytes: maxToolResultBytes(cfg.Sessions.MaxToolResultTokens)},
	}, agentruntime.Dependencies{
		Model: client, Tools: catalog, Policy: Policy{Allowed: operatorAllowlist(cfg.Tools.AllowedWriteTools)}, Transcript: PGTranscript{Pool: deps.Postgres}, Events: deps.Events,
		Compactor: agentruntime.ModelCompactor{Client: compactClient, Model: compactModel}, Artifacts: artifacts,
	})
	if err != nil {
		return Profile{}, err
	}
	promptPolicy := safety.PromptPolicy{WorkspaceRoots: cfg.Security.WorkspaceRoots, IncludeRepositoryInventory: true, Redis: deps.Redis}
	return Profile{
		Agent: Agent{Runtime: runner}, Prompt: promptPolicy,
		Redactor: safety.Redactor{WorkspaceRoots: cfg.Security.WorkspaceRoots}, Tools: catalog,
		Rates: CostRates(cfg),
	}, nil
}

func operatorAllowlist(names []string) map[string]bool {
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = true
		}
	}
	return allowed
}

func buildModelClient(provider, protocol, baseURL, apiKey string, timeout time.Duration, anthropicFlavor string) llm.Client {
	// Keep provider selection at the hosted boundary. The v2 runtime only sees
	// canonical model events through hostedmodel.Model.
	var client llm.Client
	switch {
	case provider == "longcat":
		client = llm.NewLongCatClient(baseURL, apiKey, timeout)
	case provider == "opencode-go":
		client = llm.NewOpenCodeGoClient(baseURL, apiKey, timeout)
	case protocol == "anthropic":
		client = llm.NewAnthropicClient(baseURL, apiKey, timeout, anthropicFlavor)
	default:
		client = llm.NewOpenAICompatibleClient(provider, baseURL, apiKey, timeout)
	}
	return llm.WrapClient(client, llm.CapabilitiesFor(provider, protocol))
}

func secondaryModelClient(cfg config.Config) (llm.Client, string) {
	if strings.TrimSpace(cfg.LLM.SecondaryProvider) == "" {
		return nil, ""
	}
	return buildModelClient(cfg.LLM.SecondaryProvider, cfg.LLM.SecondaryProtocol, cfg.LLM.SecondaryBaseURL, cfg.LLM.SecondaryAPIKey, cfg.LLM.Timeout, ""), cfg.LLM.SecondaryModel
}

func maxToolResultBytes(tokens int) int {
	if tokens <= 0 {
		return 64 << 10
	}
	return tokens * 4
}

func CostRates(cfg config.Config) observability.CostRates {
	rates := observability.DefaultCostRates(cfg.LLM.Provider, cfg.LLM.Model)
	if cfg.Observing.InputCostPerMTok >= 0 {
		rates.InputPerMTok = cfg.Observing.InputCostPerMTok
	}
	if cfg.Observing.OutputCostPerMTok >= 0 {
		rates.OutputPerMTok = cfg.Observing.OutputCostPerMTok
	}
	if cfg.Observing.CacheReadCostPerMTok >= 0 {
		rates.CacheReadPerMTok = cfg.Observing.CacheReadCostPerMTok
	}
	if cfg.Observing.CacheCreationCostPerMTok >= 0 {
		rates.CacheCreationPerMTok = cfg.Observing.CacheCreationCostPerMTok
	}
	return rates
}

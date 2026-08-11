package hosted

import (
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	agentruntime "github.com/noknov/slack-copilot-agent/packages/agent/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	"github.com/noknov/slack-copilot-agent/packages/providers"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
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
	Tools      *tool.Catalog
	Postgres   *pgxpool.Pool
	Redis      *redisclient.Client
	ToolSpills registry.ToolSpillStore
	Events     transcript.Sink
}

func NewProfile(cfg config.Config, deps ProfileDependencies) (Profile, error) {
	primary, err := buildModelClient(cfg.LLM.Provider, cfg.LLM.Protocol, cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Timeout, cfg.LLM.AnthropicFlavor)
	if err != nil {
		return Profile{}, err
	}
	secondary, secondaryModel, err := secondaryModelClient(cfg)
	if err != nil {
		return Profile{}, err
	}
	catalog := deps.Tools
	if catalog == nil {
		return Profile{}, fmt.Errorf("hosted tool catalog is required")
	}
	artifacts := PGArtifactStore{Store: deps.ToolSpills}
	if deps.ToolSpills != nil {
		if err := catalog.Register(ArtifactReadTool{Store: deps.ToolSpills}); err != nil {
			return Profile{}, err
		}
	}
	client := model.Client(primary)
	compactClient, compactModel := client, cfg.Sessions.CompactModel
	if secondary != nil {
		compactClient = secondary
		if compactModel == "" {
			compactModel = secondaryModel
		}
	}
	if compactModel == "" {
		compactModel = cfg.LLM.Model
	}
	runner, err := agentruntime.New(agentruntime.Config{
		Model: cfg.LLM.Model, ReasoningEffort: cfg.LLM.Thinking, Temperature: cfg.LLM.Temperature,
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

func buildModelClient(provider, protocol, baseURL, apiKey string, timeout time.Duration, anthropicFlavor string) (model.Client, error) {
	return providers.New(providers.Config{Provider: provider, Protocol: protocol, BaseURL: baseURL, APIKey: apiKey, Timeout: timeout, AnthropicFlavor: anthropicFlavor})
}

func secondaryModelClient(cfg config.Config) (model.Client, string, error) {
	if strings.TrimSpace(cfg.LLM.SecondaryProvider) == "" {
		return nil, "", nil
	}
	client, err := buildModelClient(cfg.LLM.SecondaryProvider, cfg.LLM.SecondaryProtocol, cfg.LLM.SecondaryBaseURL, cfg.LLM.SecondaryAPIKey, cfg.LLM.Timeout, "")
	return client, cfg.LLM.SecondaryModel, err
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

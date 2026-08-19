package hosted

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/slack-copilot-agent/packages/agent/environment"
	"github.com/noknov/slack-copilot-agent/packages/agent/delegation"
	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	agentruntime "github.com/noknov/slack-copilot-agent/packages/agent/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/connections"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	"github.com/noknov/slack-copilot-agent/packages/providers"
	"github.com/noknov/slack-copilot-agent/packages/safety"
)

// Profile owns the complete hosted-agent composition. Product entrypoints
// depend on this profile instead of constructing a second agent runtime first.
type Profile struct {
	Agent              Agent
	Prompt             safety.PromptPolicy
	Redactor           safety.Redactor
	Tools              *tool.Catalog
	Rates              observability.CostRates
	SecondaryModel     model.Client
	SecondaryModelName string
}

type ProfileDependencies struct {
	Tools                   *tool.Catalog
	Postgres                *pgxpool.Pool
	Redis                   *redisclient.Client
	ToolSpills              ToolSpillStore
	Events                  transcript.Sink
	Metrics                 *observability.Recorder
	ConnectionContinuations connections.ContinuationStore
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
	compactClient, compactModel := model.Client(observedModel{Client: client, Metrics: deps.Metrics}), cfg.Sessions.CompactModel
	if secondary != nil {
		secondary = observedModel{Client: secondary, Metrics: deps.Metrics}
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
		MaxOutputTokens: cfg.LLM.MaxOutputTokens, MaxSteps: cfg.Tools.AgentMaxSteps, MaxModelRetries: 2,
		Context:        agentruntime.ContextConfig{MaxTokens: cfg.Sessions.MaxContextTokens, ReserveTokens: cfg.Sessions.AutocompactBuffer},
		ToolResults:    agentruntime.ToolResultConfig{MaxInlineBytes: maxToolResultBytes(cfg.Sessions.MaxToolResultTokens)},
		CircuitBreaker: agentruntime.CircuitBreakerConfig{Enabled: true},
	}, agentruntime.Dependencies{
		Model: client, Tools: catalog, Policy: Policy{Allowed: operatorAllowlist(cfg.Tools.AllowedWriteTools)}, Transcript: PGTranscript{Pool: deps.Postgres}, Events: deps.Events,
		Compactor: agentruntime.ModelCompactor{Client: compactClient, Model: compactModel, MaxInputTokens: cfg.Sessions.MaxContextTokens - cfg.Sessions.AutocompactBuffer}, Artifacts: artifacts,
		Environment: environment.Config{WorkspaceRoots: cfg.Security.WorkspaceRoots},
		ConnectionContinuations: deps.ConnectionContinuations,
	})
	if err != nil {
		return Profile{}, err
	}
	exploreRunner := delegation.Runner{
		Config: agentruntime.Config{
			Model: cfg.LLM.Model, ReasoningEffort: cfg.LLM.Thinking, Temperature: cfg.LLM.Temperature,
			MaxOutputTokens: cfg.LLM.MaxOutputTokens, MaxSteps: 12,
			Context:         agentruntime.ContextConfig{MaxTokens: cfg.Sessions.MaxContextTokens, ReserveTokens: cfg.Sessions.AutocompactBuffer},
			ToolResults:     agentruntime.ToolResultConfig{MaxInlineBytes: maxToolResultBytes(cfg.Sessions.MaxToolResultTokens)},
		},
		Deps: agentruntime.Dependencies{
			Model: client, Policy: Policy{Allowed: operatorAllowlist(cfg.Tools.AllowedWriteTools)},
			Compactor: agentruntime.ModelCompactor{Client: compactClient, Model: compactModel, MaxInputTokens: cfg.Sessions.MaxContextTokens - cfg.Sessions.AutocompactBuffer},
			Artifacts: artifacts, Environment: environment.Config{WorkspaceRoots: cfg.Security.WorkspaceRoots},
		},
		ParentCatalog: catalog,
		AllowedTools:  delegation.DefaultHostedAllowedTools(),
	}
	if err := catalog.Register(delegation.ExploreTool{Runner: exploreRunner}); err != nil {
		return Profile{}, err
	}
	promptPolicy := safety.PromptPolicy{}
	return Profile{
		Agent: Agent{Runtime: runner}, Prompt: promptPolicy,
		Redactor: safety.Redactor{WorkspaceRoots: cfg.Security.WorkspaceRoots}, Tools: catalog,
		Rates: CostRates(cfg), SecondaryModel: secondary, SecondaryModelName: secondaryModel,
	}, nil
}

type observedModel struct {
	Client  model.Client
	Metrics *observability.Recorder
}

func (c observedModel) Generate(ctx context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	started := time.Now()
	response, err := c.Client.Generate(ctx, request, sink)
	if c.Metrics != nil {
		metricErr := err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			metricErr = nil
		}
		c.Metrics.LLMCall(observability.UsageFromModel(response.Usage), time.Since(started), metricErr)
	}
	return response, err
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
	rates := observability.CostRates{}
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

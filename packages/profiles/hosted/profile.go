package hosted

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	agentremote "github.com/noknov/kepler-agent/packages/a2a"
	"github.com/noknov/kepler-agent/packages/agent/delegation"
	"github.com/noknov/kepler-agent/packages/agent/environment"
	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/infra/redisclient"
	"github.com/noknov/kepler-agent/packages/infra/telemetry"
	"github.com/noknov/kepler-agent/packages/memory"
	"github.com/noknov/kepler-agent/packages/observability"
	"github.com/noknov/kepler-agent/packages/providers"
	"github.com/noknov/kepler-agent/packages/safety"
	memorytools "github.com/noknov/kepler-agent/packages/tools/memory"
)

// Profile owns the complete hosted-agent composition. Product entrypoints
// depend on this profile instead of constructing a second agent runtime first.
type Profile struct {
	Capabilities       func(context.Context, string) (providers.ModelCapabilities, error)
	Agent              Agent
	Prompt             safety.PromptPolicy
	Redactor           safety.Redactor
	Tools              *tool.Catalog
	Rates              observability.CostRates
	SecondaryModel     model.Client
	SecondaryModelName string
}

type ProfileDependencies struct {
	ModelGovernor           *model.Governor
	Tools                   *tool.Catalog
	Postgres                *pgxpool.Pool
	Redis                   *redisclient.Client
	ToolSpills              ToolSpillStore
	Events                  transcript.Sink
	Metrics                 *observability.Recorder
	ConnectionContinuations agentruntime.ConnectionContinuationStore
	Lease                   agentruntime.SessionLease
}

func NewProfile(cfg config.Config, deps ProfileDependencies) (Profile, error) {
	primary, err := buildModelClient(cfg.LLM.Provider, cfg.LLM.Protocol, cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Timeout, cfg.LLM.AnthropicFlavor, cfg.LLM.ChatGPTCredentialsFile)
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
	if err := agentremote.Register(catalog, PGTranscript{Pool: deps.Postgres}, cfg.RemoteAgents); err != nil {
		return Profile{}, err
	}
	var memoryStore memory.Store
	if deps.Postgres != nil {
		memoryStore = memory.PGStore{Pool: deps.Postgres, MaxRecords: cfg.Memory.MaxRecords}
		if err := catalog.Register(memorytools.Tool{Store: memoryStore}); err != nil {
			return Profile{}, err
		}
	}
	artifacts := PGArtifactStore{Store: deps.ToolSpills}
	if deps.ToolSpills != nil {
		if err := catalog.Register(ArtifactReadTool{Store: deps.ToolSpills, MaxInlineBytes: maxToolResultBytes(cfg.Sessions.MaxToolResultTokens)}); err != nil {
			return Profile{}, err
		}
	}
	governor := deps.ModelGovernor
	if governor == nil {
		governor = model.NewGovernor(cfg.Tools.AgentMaxConcurrentModelCalls)
	}
	breakers := model.NewCircuitBreakers()
	primaryObserved := model.Client(observedModel{Client: primary, Metrics: deps.Metrics})
	primaryClient := resilientModel(cfg, breakers, primaryObserved, cfg.LLM.Provider, nil, "", "")
	client := primaryClient
	exploreClient, exploreModel := client, cfg.LLM.Model
	compactClient, compactModel := client, cfg.Sessions.CompactModel
	var secondaryObserved model.Client
	if secondary != nil {
		secondaryObserved = observedModel{Client: secondary, Metrics: deps.Metrics}
		client = resilientModel(cfg, breakers, primaryObserved, cfg.LLM.Provider, secondaryObserved, cfg.LLM.SecondaryProvider, secondaryModel)
		exploreClient = resilientModel(cfg, breakers, secondaryObserved, cfg.LLM.SecondaryProvider, primaryObserved, cfg.LLM.Provider, cfg.LLM.Model)
		exploreModel = secondaryModel
		compactClient = resilientModel(cfg, breakers, secondaryObserved, cfg.LLM.SecondaryProvider, nil, "", "")
		if compactModel == "" {
			compactModel = secondaryModel
		}
	}
	if compactModel == "" {
		compactModel = cfg.LLM.Model
	}
	var nativeCompact model.Client
	if cfg.LLM.NativeCompaction {
		nativeCompact = primaryClient
	}
	runner, err := agentruntime.New(agentruntime.Config{
		MaxTotalTokens: cfg.Tools.AgentMaxTotalTokens,
		Model:          cfg.LLM.Model, ReasoningEffort: cfg.LLM.Thinking, Temperature: cfg.LLM.Temperature,
		MaxOutputTokens: cfg.LLM.MaxOutputTokens, MaxSteps: cfg.Tools.AgentMaxSteps, MaxParallelToolCalls: cfg.Tools.AgentMaxParallelToolCalls, MaxModelRetries: 0, MaxEmptyResponseRetries: 3,
		Context:        agentruntime.ContextConfig{MaxTokens: cfg.Sessions.MaxContextTokens, ReserveTokens: cfg.Sessions.AutocompactBuffer, OutputTokens: cfg.LLM.MaxOutputTokens},
		ToolResults:    agentruntime.ToolResultConfig{MaxInlineBytes: maxToolResultBytes(cfg.Sessions.MaxToolResultTokens)},
		CircuitBreaker: agentruntime.CircuitBreakerConfig{Enabled: true},
	}, agentruntime.Dependencies{
		ModelGovernor: governor,
		TraceContent:  telemetry.ContentRecorder(),
		Model:         client, Tools: catalog, Policy: Policy{Allowed: operatorAllowlist(cfg.Tools.AllowedWriteTools)}, Transcript: PGTranscript{Pool: deps.Postgres}, Events: deps.Events, Lease: deps.Lease,
		Compactor: agentruntime.ModelCompactor{NativeClient: nativeCompact, NativeModel: cfg.LLM.Model, Governor: governor, Client: compactClient, Model: compactModel, MaxInputTokens: cfg.Sessions.MaxContextTokens - cfg.Sessions.AutocompactBuffer}, Artifacts: artifacts,
		Environment:             environment.Config{WorkspaceRoots: cfg.Security.WorkspaceRoots},
		ConnectionContinuations: deps.ConnectionContinuations,
	})
	if err != nil {
		return Profile{}, err
	}
	exploreRunner := delegation.Runner{
		Config: agentruntime.Config{
			Model: exploreModel, ReasoningEffort: cfg.LLM.Thinking, Temperature: cfg.LLM.Temperature,
			MaxOutputTokens: cfg.LLM.MaxOutputTokens, MaxSteps: cfg.Tools.AgentExploreMaxSteps,
			Context:     agentruntime.ContextConfig{MaxTokens: cfg.Sessions.MaxContextTokens, ReserveTokens: cfg.Sessions.AutocompactBuffer, OutputTokens: cfg.LLM.MaxOutputTokens},
			ToolResults: agentruntime.ToolResultConfig{MaxInlineBytes: maxToolResultBytes(cfg.Sessions.MaxToolResultTokens)},
		},
		Deps: agentruntime.Dependencies{
			ModelGovernor: governor,
			TraceContent:  telemetry.ContentRecorder(),
			Model:         exploreClient, Policy: Policy{Allowed: operatorAllowlist(cfg.Tools.AllowedWriteTools)}, Lease: deps.Lease,
			Transcript: PGTranscript{Pool: deps.Postgres}, Events: deps.Events,
			Compactor: agentruntime.ModelCompactor{NativeClient: nativeCompact, NativeModel: cfg.LLM.Model, Governor: governor, Client: compactClient, Model: compactModel, MaxInputTokens: cfg.Sessions.MaxContextTokens - cfg.Sessions.AutocompactBuffer},
			Artifacts: artifacts, Environment: environment.Config{WorkspaceRoots: cfg.Security.WorkspaceRoots},
		},
		ParentCatalog: catalog,
		AllowedTools:  delegation.DefaultHostedAllowedTools(),
		MaxSteps:      cfg.Tools.AgentExploreMaxSteps,
		MaxWorkers:    cfg.Tools.AgentExploreMaxWorkers,
		Budget: delegation.ExecutionBudget{
			WorkerTimeout: cfg.Tools.AgentExploreWorkerTimeout,
			BatchTimeout:  cfg.Tools.AgentExploreBatchTimeout,
		},
	}
	if err := catalog.Register(delegation.EvidenceTool{Store: PGTranscript{Pool: deps.Postgres}}); err != nil {
		return Profile{}, err
	}
	if err := catalog.Register(delegation.ExploreTool{Runner: exploreRunner}); err != nil {
		return Profile{}, err
	}
	var memoryRanker memory.Ranker
	if cfg.Memory.Rerank {
		memoryRanker = memory.ModelRanker{Client: client, Model: cfg.LLM.Model, Governor: governor, CountTokens: agentruntime.EstimateTokens}
	}
	promptPolicy := safety.PromptPolicy{}
	return Profile{
		Capabilities: func(ctx context.Context, name string) (providers.ModelCapabilities, error) {
			return primary.Capabilities(ctx, cfg.LLM.Provider, name)
		},
		Agent: Agent{Runtime: runner, Memory: memoryStore, MemoryRanker: memoryRanker, MemoryTimeout: cfg.Memory.Timeout}, Prompt: promptPolicy,
		Redactor: safety.Redactor{WorkspaceRoots: cfg.Security.WorkspaceRoots}, Tools: catalog,
		Rates: CostRates(cfg), SecondaryModel: model.Govern(secondaryObserved, governor), SecondaryModelName: secondaryModel,
	}, nil
}

// resilientModel is the sole retry, circuit-breaker, and failover boundary for
// a request path. Its inputs must be provider clients rather than other
// ResilientClients; nesting policies multiplies attempts and obscures budgets.
func resilientModel(cfg config.Config, breakers *model.CircuitBreakers, primary model.Client, primaryProvider string, fallback model.Client, fallbackProvider, fallbackModel string) model.Client {
	// A fallback to the same configured route only repeats the primary attempt.
	// Keep explicit retries under MaxAttempts instead of adding a second budget.
	if primaryProvider == fallbackProvider && fallbackModel == cfg.LLM.Model && sameModelRoute(cfg.LLM) {
		fallback = nil
		fallbackProvider, fallbackModel = "", ""
	}
	return &model.ResilientClient{
		Primary: primary, PrimaryProvider: primaryProvider,
		Fallback: fallback, FallbackProvider: fallbackProvider, FallbackModel: fallbackModel,
		MaxAttempts: cfg.LLM.Resilience.MaxAttempts, RetryDelay: cfg.LLM.Resilience.RetryBaseDelay,
		MinAttemptBudget: cfg.LLM.Resilience.MinAttemptBudget, FailureThreshold: cfg.LLM.Resilience.FailureThreshold,
		Cooldown: cfg.LLM.Resilience.CircuitCooldown,
		Breakers: breakers,
	}
}

func sameModelRoute(cfg config.LLMConfig) bool {
	// These are the fields passed to the two provider constructors. Credentials
	// file and timeout are shared; the secondary has no Anthropic flavor override.
	return cfg.Provider != "" && cfg.Provider == cfg.SecondaryProvider &&
		cfg.Model == cfg.SecondaryModel && cfg.Protocol == cfg.SecondaryProtocol &&
		cfg.BaseURL == cfg.SecondaryBaseURL && cfg.APIKey == cfg.SecondaryAPIKey &&
		(!strings.EqualFold(cfg.Protocol, "anthropic") || cfg.AnthropicFlavor == "")
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

func buildModelClient(provider, protocol, baseURL, apiKey string, timeout time.Duration, anthropicFlavor string, credentialFile string) (*providers.Client, error) {
	return providers.New(providers.Config{
		ChatGPTCredentialsFile: credentialFile,
		Provider:               provider,
		Protocol:               protocol,
		BaseURL:                baseURL,
		APIKey:                 apiKey,
		Timeout:                timeout,
		AnthropicFlavor:        anthropicFlavor,
	})
}

func secondaryModelClient(cfg config.Config) (model.Client, string, error) {
	if strings.TrimSpace(cfg.LLM.SecondaryProvider) == "" {
		return nil, "", nil
	}
	client, err := buildModelClient(cfg.LLM.SecondaryProvider, cfg.LLM.SecondaryProtocol, cfg.LLM.SecondaryBaseURL, cfg.LLM.SecondaryAPIKey, cfg.LLM.Timeout, "", cfg.LLM.ChatGPTCredentialsFile)
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

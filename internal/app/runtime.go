package app

import (
	"log"
	"time"

	"github.com/wati/oncall-agent/internal/agent"
	"github.com/wati/oncall-agent/internal/config"
	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/memory"
	"github.com/wati/oncall-agent/internal/observability"
	"github.com/wati/oncall-agent/internal/rag"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/slack"
	ragTools "github.com/wati/oncall-agent/internal/toolkit/tools/rag"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type agentRuntime struct {
	Runner     agent.Runner
	Memory     memory.Builder
	Prompt     safety.PromptPolicy
	Redactor   safety.Redactor
	Tools      *registry.Registry
	CostRates  observability.CostRates
	RAGManager *rag.Manager
}

func newAgentRuntime(cfg config.Config, slackClient *slack.Client, recorder *observability.Recorder) agentRuntime {
	llmClient := newLLMClient(cfg)
	llmCapabilities := llm.CapabilitiesFor(cfg.LLM.Provider, cfg.LLM.Protocol)
	workspacePolicy := safety.WorkspacePolicy{Roots: cfg.Security.WorkspaceRoots}
	commandPolicy := safety.NewCommandPolicy()
	redactor := safety.Redactor{WorkspaceRoots: cfg.Security.WorkspaceRoots}
	promptPolicy := safety.PromptPolicy{
		WorkspaceRoots:             cfg.Security.WorkspaceRoots,
		IncludeRepositoryInventory: cfg.Security.PromptIncludeRepoInventory,
	}
	mem := memory.Builder{
		MaxContextTokens: cfg.Sessions.MaxContextTokens,
	}
	secondaryClient, secondaryModel := newSecondaryLLMClient(cfg)
	tools := newToolRegistry(cfg, slackClient, llmClient, secondaryClient, secondaryModel, workspacePolicy, commandPolicy)

	var statusSummarizer *agent.StatusSummarizer
	if cfg.LLM.DynamicStatus {
		summaryClient, summaryModel := secondaryClient, secondaryModel
		if summaryClient == nil {
			summaryClient, summaryModel = llmClient, cfg.LLM.Model
		}
		// 10s timeout: primary model requests can take 3-8s under load;
		// 5s was too tight and caused near-constant silent timeouts.
		statusSummarizer = &agent.StatusSummarizer{
			Client:  summaryClient,
			Model:   summaryModel,
			Timeout: 10 * time.Second,
		}
	}

	// Build the 4-layer context compactor.
	compactModel := cfg.Sessions.CompactModel
	if compactModel == "" {
		compactModel = secondaryModel
	}
	compactClient := llmClient
	if secondaryClient != nil && compactModel != "" {
		compactClient = secondaryClient
	}
	if compactModel == "" {
		compactModel = cfg.LLM.Model
	}
	compactor := &memory.Compactor{
		MaxContextTokens:    cfg.Sessions.MaxContextTokens,
		AutocompactBuffer:   cfg.Sessions.AutocompactBuffer,
		OutputReserve:       memory.DefaultOutputReserve,
		KeepRecentTools:     memory.DefaultKeepRecentTools,
		MaxToolResultTokens: cfg.Sessions.MaxToolResultTokens,
		ClearableTools:      defaultClearableTools(),
		LLMClient:           compactClient,
		CompactModel:        compactModel,
	}

	var ragMgr *rag.Manager
	if cfg.RAG.Enabled {
		var err error
		ragMgr, err = rag.NewManager(rag.Config{
			PostgresDSN:      cfg.RAG.PostgresDSN,
			EmbeddingBaseURL: cfg.RAG.EmbeddingBaseURL,
			EmbeddingAPIKey:  cfg.RAG.EmbeddingAPIKey,
			EmbeddingModel:   cfg.RAG.EmbeddingModel,
			EmbeddingDims:    cfg.RAG.EmbeddingDims,
			BatchDelay:       cfg.RAG.BatchDelay,
			IndexInterval:    cfg.RAG.IndexInterval,
			WorkspaceRoots:   cfg.Security.WorkspaceRoots,
			Observer:         recorder,
		})
		if err != nil {
			log.Printf("rag: failed to initialize, continuing without RAG: %v", err)
		} else {
			tools.Register(ragTools.SearchTool{Manager: ragMgr, Paths: workspacePolicy, Observer: recorder})
		}
	}

	return agentRuntime{
		Runner: agent.Runner{
			LLM:              llmClient,
			Model:            cfg.LLM.Model,
			Thinking:         cfg.LLM.Thinking,
			MaxTokens:        cfg.LLM.MaxTokens,
			Temp:             cfg.LLM.Temperature,
			Tools:            tools,
			Capabilities:     llmCapabilities,
			Format:           mem,
			Sanitize:         redactor,
			Observer:         recorder,
			MaxSteps:         cfg.Tools.AgentMaxSteps,
			Compactor:        compactor,
			StatusSummarizer: statusSummarizer,
		},
		Memory:     mem,
		Prompt:     promptPolicy,
		Redactor:   redactor,
		Tools:      tools,
		CostRates:  costRates(cfg),
		RAGManager: ragMgr,
	}
}

func newLLMClient(cfg config.Config) llm.Client {
	return buildLLMClient(cfg.LLM.Provider, cfg.LLM.Protocol, cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Timeout, cfg.LLM.AnthropicFlavor)
}

func newSecondaryLLMClient(cfg config.Config) (llm.Client, string) {
	if cfg.LLM.SecondaryProvider == "" {
		return nil, ""
	}
	client := buildLLMClient(cfg.LLM.SecondaryProvider, cfg.LLM.SecondaryProtocol, cfg.LLM.SecondaryBaseURL, cfg.LLM.SecondaryAPIKey, cfg.LLM.Timeout, "")
	return client, cfg.LLM.SecondaryModel
}

func buildLLMClient(provider, protocol, baseURL, apiKey string, timeout time.Duration, anthropicFlavor string) llm.Client {
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

func costRates(cfg config.Config) observability.CostRates {
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

// defaultClearableTools returns the set of tool names whose results can be
// cleared by the Layer 1 micro-compact. These are read/search/browse tools
// that produce large outputs the model has already incorporated.
func defaultClearableTools() map[string]bool {
	tools := []string{
		// Code search and reading
		"code-read_file", "code-search", "code-symbols",
		"code-definition", "code-references", "code-implementation",
		"code-incoming_calls", "code-outgoing_calls", "code-diagnostics", "explore-code",
		"tool_spill-read",
		// Git reading
		"git-search_ref", "git-read_file_ref", "git-log", "git-show",
		"repo-search", "repo-read_file",
		// Web and knowledge
		"web-read_page", "knowledge-runbook_search", "rag-search",
		// Diagnostics
		"diagnostics-timeline", "diagnostics-incident_brief", "diagnostics-evidence_board",
		// GCP logs
		"gcp-logs",
		// Slack file search
		"slack-file_search", "slack-json_analyze",
		// GitHub
		"github-pr_diff", "github-workflow_runs",
		// Notion
		"notion-search",
		// Playwright / browser
		"pw-screenshot", "pw-snapshot",
		"browser_snapshot", "browser_take_screenshot",
	}
	m := make(map[string]bool, len(tools))
	for _, t := range tools {
		m[t] = true
	}
	return m
}

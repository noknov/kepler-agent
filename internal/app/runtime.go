package app

import (
	"log"

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
		MaxMessages:     cfg.Sessions.MaxMessages,
		MaxToolChars:    cfg.Sessions.MaxToolChars,
		MaxThreadChars:  cfg.Sessions.MaxThreadChars,
		MaxSummaryChars: cfg.Sessions.MaxSummaryChars,
	}
	tools := newToolRegistry(cfg, slackClient, llmClient, workspacePolicy, commandPolicy)

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
			LLM:          llmClient,
			Model:        cfg.LLM.Model,
			Thinking:     cfg.LLM.Thinking,
			MaxTokens:    cfg.LLM.MaxTokens,
			Temp:         cfg.LLM.Temperature,
			Tools:        tools,
			Capabilities: llmCapabilities,
			Format:       mem,
			Sanitize:     redactor,
			Observer:     recorder,
			MaxSteps:     cfg.Tools.AgentMaxSteps,
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
	var client llm.Client
	if cfg.LLM.Provider == "opencode-go" {
		client = llm.NewOpenCodeGoClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Timeout)
	} else if cfg.LLM.Protocol == "anthropic" {
		client = llm.NewAnthropicClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Timeout, cfg.LLM.AnthropicFlavor)
	} else {
		client = llm.NewKimiClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Timeout)
	}
	return llm.WrapClient(client, llm.CapabilitiesFor(cfg.LLM.Provider, cfg.LLM.Protocol))
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

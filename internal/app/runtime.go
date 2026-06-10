package app

import (
	"path/filepath"

	"github.com/wati/oncall-agent/internal/agent"
	"github.com/wati/oncall-agent/internal/codeintel"
	"github.com/wati/oncall-agent/internal/config"
	"github.com/wati/oncall-agent/internal/delegation"
	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/memory"
	"github.com/wati/oncall-agent/internal/observability"
	"github.com/wati/oncall-agent/internal/prompts"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/slack"
	codeTools "github.com/wati/oncall-agent/internal/toolkit/tools/code"
	codeIntelTools "github.com/wati/oncall-agent/internal/toolkit/tools/codeintel"
	diagnosticsTools "github.com/wati/oncall-agent/internal/toolkit/tools/diagnostics"
	gcpTools "github.com/wati/oncall-agent/internal/toolkit/tools/gcp"
	gitTools "github.com/wati/oncall-agent/internal/toolkit/tools/git"
	githubTools "github.com/wati/oncall-agent/internal/toolkit/tools/github"
	knowledgeTools "github.com/wati/oncall-agent/internal/toolkit/tools/knowledge"
	luckinTools "github.com/wati/oncall-agent/internal/toolkit/tools/luckin"
	notionTools "github.com/wati/oncall-agent/internal/toolkit/tools/notion"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
	skillTools "github.com/wati/oncall-agent/internal/toolkit/tools/skills"
	"github.com/wati/oncall-agent/internal/toolkit/tools/slacktool"
	webSearchTools "github.com/wati/oncall-agent/internal/toolkit/tools/websearch"
	youtrackTools "github.com/wati/oncall-agent/internal/toolkit/tools/youtrack"
)

type agentRuntime struct {
	Runner    agent.Runner
	Memory    memory.Builder
	Prompt    safety.PromptPolicy
	Redactor  safety.Redactor
	Tools     *registry.Registry
	CostRates observability.CostRates
}

func newAgentRuntime(cfg config.Config, slackClient *slack.Client, recorder *observability.Recorder) agentRuntime {
	llmClient := newLLMClient(cfg)
	workspacePolicy := safety.WorkspacePolicy{Roots: cfg.Security.WorkspaceRoots}
	commandPolicy := safety.NewCommandPolicy()
	redactor := safety.Redactor{WorkspaceRoots: cfg.Security.WorkspaceRoots}
	promptPolicy := safety.PromptPolicy{WorkspaceRoots: cfg.Security.WorkspaceRoots}
	mem := memory.Builder{
		MaxMessages:     cfg.Sessions.MaxMessages,
		MaxToolChars:    cfg.Sessions.MaxToolChars,
		MaxThreadChars:  cfg.Sessions.MaxThreadChars,
		MaxSummaryChars: cfg.Sessions.MaxSummaryChars,
	}
	tools := newToolRegistry(cfg, slackClient, llmClient, workspacePolicy, commandPolicy)
	return agentRuntime{
		Runner: agent.Runner{
			LLM:       llmClient,
			Model:     cfg.LLM.Model,
			Thinking:  cfg.LLM.Thinking,
			MaxTokens: cfg.LLM.MaxTokens,
			Temp:      cfg.LLM.Temperature,
			Tools:     tools,
			Format:    mem,
			Sanitize:  redactor,
			Observer:  recorder,
			MaxSteps:  cfg.Tools.AgentMaxSteps,
		},
		Memory:    mem,
		Prompt:    promptPolicy,
		Redactor:  redactor,
		Tools:     tools,
		CostRates: costRates(cfg),
	}
}

func newLLMClient(cfg config.Config) llm.Client {
	var client llm.Client
	if cfg.LLM.Protocol == "anthropic" {
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

func newToolRegistry(cfg config.Config, slackClient *slack.Client, llmClient llm.Client, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy) *registry.Registry {
	delegates := delegation.NewManager(llmClient, cfg.LLM.Model, cfg.LLM.Thinking)
	_ = delegates.LoadMarkdown(filepath.Join(prompts.Dir(), "rules"), filepath.Join(prompts.Dir(), "skills"))

	tools := registry.New()
	tools.Register(diagnosticsTools.IncidentBriefTool{})
	tools.Register(diagnosticsTools.TimelineTool{})
	tools.Register(diagnosticsTools.EvidenceBoardTool{})
	intel := codeintel.Manager{Paths: workspacePolicy, Timeout: cfg.Tools.CommandTimeout}
	tools.Register(codeIntelTools.SymbolsTool{Manager: intel})
	tools.Register(codeIntelTools.DefinitionTool{Manager: intel})
	tools.Register(codeIntelTools.ReferencesTool{Manager: intel})
	tools.Register(codeIntelTools.DiagnosticsTool{Manager: intel})
	tools.Register(codeTools.SearchTool{Paths: workspacePolicy})
	tools.Register(codeTools.ReadFileTool{Paths: workspacePolicy})
	gitBase := gitTools.Base{Paths: workspacePolicy, Guard: commandPolicy, Timeout: cfg.Tools.CommandTimeout}
	tools.Register(gitTools.RepoSearchTool{Base: gitBase})
	tools.Register(gitTools.RepoReadFileTool{Base: gitBase})
	tools.Register(gitTools.FetchRefTool{Base: gitBase})
	tools.Register(gitTools.SearchRefTool{Base: gitBase})
	tools.Register(gitTools.ReadFileRefTool{Base: gitBase})
	tools.Register(gitTools.StatusTool{Base: gitBase})
	tools.Register(gitTools.LogTool{Base: gitBase})
	tools.Register(gitTools.ShowTool{Base: gitBase})
	tools.Register(gcpTools.LogsTool{
		GCloudPath:       cfg.Tools.GCloudPath,
		DefaultProject:   cfg.Tools.GCPDefaultProject,
		DefaultNamespace: cfg.Tools.GCPDefaultNamespace,
		DefaultCluster:   cfg.Tools.GKEDefaultCluster,
		DefaultRegion:    cfg.Tools.GKEDefaultRegion,
		Guard:            commandPolicy,
		Timeout:          cfg.Tools.CommandTimeout,
	})
	notionClient := notionTools.Client{
		Token:         cfg.Tools.NotionToken,
		DatabaseID:    cfg.Tools.NotionDatabaseID,
		TitleProperty: cfg.Tools.NotionTitleProperty,
		Version:       cfg.Tools.NotionVersion,
	}
	tools.Register(notionTools.SearchTool{Client: notionClient})
	tools.Register(notionTools.CreatePageTool{Client: notionClient})
	youtrackClient := youtrackTools.Client{BaseURL: cfg.Tools.YouTrackURL, Token: cfg.Tools.YouTrackToken}
	tools.Register(youtrackTools.GetIssueTool{Client: youtrackClient})
	tools.Register(youtrackTools.SearchTool{Client: youtrackClient})
	githubClient := githubTools.Client{
		Token:      cfg.Tools.GitHubToken,
		APIBaseURL: cfg.Tools.GitHubAPIBaseURL,
		Owner:      cfg.Tools.GitHubDefaultOwner,
		Repo:       cfg.Tools.GitHubDefaultRepo,
	}
	tools.Register(githubTools.DispatchWorkflowTool{Client: githubClient})
	tools.Register(githubTools.WorkflowRunsTool{Client: githubClient})
	tools.Register(githubTools.PRDiffTool{Client: githubClient})
	luckinTools.RegisterAll(tools, &luckinTools.Client{
		URL:   cfg.Tools.LuckinMCPURL,
		Token: cfg.Tools.LuckinMCPToken,
	})
	tools.Register(webSearchTools.ReadPageTool{Client: webSearchTools.Client{}})
	tools.Register(knowledgeTools.RunbookSearchTool{})
	tools.Register(skillTools.LoadTool{})
	tools.Register(slacktool.AskUserTool{Slack: slackClient})
	tools.Register(slacktool.FileSearchTool{Slack: slackClient})
	tools.Register(slacktool.JSONAnalyzeTool{Slack: slackClient})
	tools.Register(delegation.Tool{Manager: delegates})
	return tools
}

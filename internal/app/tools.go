package app

import (
	"github.com/wati/oncall-agent/internal/codeintel"
	"github.com/wati/oncall-agent/internal/config"
	"github.com/wati/oncall-agent/internal/delegation"
	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/mcp"
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
	plannerTools "github.com/wati/oncall-agent/internal/toolkit/tools/planner"
	playwrightTools "github.com/wati/oncall-agent/internal/toolkit/tools/playwright"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
	skillTools "github.com/wati/oncall-agent/internal/toolkit/tools/skills"
	"github.com/wati/oncall-agent/internal/toolkit/tools/slacktool"
	webSearchTools "github.com/wati/oncall-agent/internal/toolkit/tools/websearch"
	youtrackTools "github.com/wati/oncall-agent/internal/toolkit/tools/youtrack"
)

func newToolRegistry(cfg config.Config, slackClient *slack.Client, llmClient llm.Client, secondaryClient llm.Client, secondaryModel string, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy) *registry.Registry {
	tools := registry.NewReadOnly()
	registerDeferredDiagnosticsTools(tools)
	registerCodeTools(tools, cfg, workspacePolicy, commandPolicy)
	registerIntegrationTools(tools, cfg, commandPolicy)
	registerKnowledgeTools(tools, cfg)
	registerSlackTools(tools, slackClient)
	registerAgentControlTools(tools, cfg, llmClient, secondaryClient, secondaryModel)
	tools.Register(registry.ToolSearchTool{Registry: tools})
	return tools
}

func registerDeferredDiagnosticsTools(tools *registry.Registry) {
	tools.RegisterDeferred(registry.AsDeferred(registry.CategoryDiagnostics, diagnosticsTools.IncidentBriefTool{}))
	tools.RegisterDeferred(registry.AsDeferred(registry.CategoryDiagnostics, diagnosticsTools.TimelineTool{}))
	tools.RegisterDeferred(registry.AsDeferred(registry.CategoryDiagnostics, diagnosticsTools.EvidenceBoardTool{}))
}

func registerCodeTools(tools *registry.Registry, cfg config.Config, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy) {
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
}

func registerIntegrationTools(tools *registry.Registry, cfg config.Config, commandPolicy safety.CommandPolicy) {
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
	if notionClient.Token != "" {
		tools.Register(notionTools.SearchTool{Client: notionClient})
		tools.Register(notionTools.GetPageTool{Client: notionClient})
		tools.Register(notionTools.QueryDatabaseTool{Client: notionClient})
	} else {
		tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, notionTools.SearchTool{Client: notionClient}))
		tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, notionTools.GetPageTool{Client: notionClient}))
		tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, notionTools.QueryDatabaseTool{Client: notionClient}))
	}

	youtrackClient := youtrackTools.Client{BaseURL: cfg.Tools.YouTrackURL, Token: cfg.Tools.YouTrackToken}
	tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, youtrackTools.GetIssueTool{Client: youtrackClient}))
	tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, youtrackTools.SearchTool{Client: youtrackClient}))

	githubClient := githubTools.Client{
		Token:      cfg.Tools.GitHubToken,
		APIBaseURL: cfg.Tools.GitHubAPIBaseURL,
		Owner:      cfg.Tools.GitHubDefaultOwner,
		Repo:       cfg.Tools.GitHubDefaultRepo,
	}
	tools.Register(githubTools.DispatchWorkflowTool{Client: githubClient})
	tools.Register(githubTools.WorkflowRunsTool{Client: githubClient})
	tools.Register(githubTools.PRDiffTool{Client: githubClient})

	luckinTools.RegisterDeferredAll(tools, &luckinTools.Client{
		MCP: &mcp.Client{
			ServiceName: "luckin",
			URL:         cfg.Tools.LuckinMCPURL,
			Token:       cfg.Tools.LuckinMCPToken,
		},
	}, registry.CategoryIntegration)

	playwrightTools.RegisterDeferredAll(tools, &playwrightTools.Client{
		MCP: &mcp.Client{
			ServiceName: "playwright",
			URL:         cfg.Tools.PlaywrightMCPURL,
			Token:       cfg.Tools.PlaywrightMCPToken,
		},
	}, registry.CategoryBrowser)
}

func registerKnowledgeTools(tools *registry.Registry, cfg config.Config) {
	webClient := webSearchTools.Client{
		Provider:       cfg.Tools.WebSearchProvider,
		GoogleAPIKey:   cfg.Tools.WebSearchGoogleKey,
		GoogleCX:       cfg.Tools.WebSearchGoogleCX,
		SerpAPIKey:     cfg.Tools.WebSearchSerpAPIKey,
		SerpAPIBaseURL: cfg.Tools.WebSearchSerpAPIURL,
		SearXNGBaseURL: cfg.Tools.WebSearchSearXNGURL,
	}
	tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, webSearchTools.ReadPageTool{Client: webClient}))
	tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, webSearchTools.SearchTool{Client: webClient}))
	tools.Register(knowledgeTools.RunbookSearchTool{})
}

func registerSlackTools(tools *registry.Registry, slackClient *slack.Client) {
	tools.Register(slacktool.AskUserTool{Slack: slackClient})
	tools.Register(slacktool.FileSearchTool{Slack: slackClient})
	tools.Register(slacktool.JSONAnalyzeTool{Slack: slackClient})
	tools.Register(slacktool.SendScreenshotTool{Slack: slackClient})
}

func registerAgentControlTools(tools *registry.Registry, cfg config.Config, llmClient llm.Client, secondaryClient llm.Client, secondaryModel string) {
	delegates := delegation.NewManager(llmClient, cfg.LLM.Model, cfg.LLM.Thinking)
	if secondaryClient != nil && secondaryModel != "" {
		delegates.SetSecondaryClient(secondaryClient, secondaryModel)
	}
	delegates.SetPolicyPrompt(prompts.RulesAndSkillsPrompt())
	delegates.SetTools(tools)
	tools.Register(plannerTools.PlanTool{})
	tools.Register(skillTools.LoadTool{})
	tools.Register(delegation.Tool{Manager: delegates})
	tools.Register(delegation.ExploreTool{Manager: delegates})
}

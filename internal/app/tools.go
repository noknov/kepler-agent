package app

import (
	agentTools "github.com/noknov/slack-copilot-agent/internal/agent"
	"github.com/noknov/slack-copilot-agent/internal/codeintel"
	"github.com/noknov/slack-copilot-agent/internal/config"
	"github.com/noknov/slack-copilot-agent/internal/delegation"
	"github.com/noknov/slack-copilot-agent/internal/llm"
	"github.com/noknov/slack-copilot-agent/internal/mcp"
	"github.com/noknov/slack-copilot-agent/internal/prompts"
	"github.com/noknov/slack-copilot-agent/internal/reminder"
	"github.com/noknov/slack-copilot-agent/internal/safety"
	"github.com/noknov/slack-copilot-agent/internal/slack"
	codeTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/code"
	codeIntelTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/codeintel"
	diagnosticsTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/diagnostics"
	gcpTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/gcp"
	gitTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/git"
	githubTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/github"
	k8sTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/k8s"
	knowledgeTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/knowledge"
	luckinTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/luckin"
	notionTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/notion"
	plannerTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/planner"
	playwrightTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/playwright"
	"github.com/noknov/slack-copilot-agent/internal/toolkit/tools/registry"
	reminderTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/reminder"
	shellTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/shell"
	skillTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/skills"
	"github.com/noknov/slack-copilot-agent/internal/toolkit/tools/slacktool"
	ttsTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/tts"
	webSearchTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/websearch"
	youtrackTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/youtrack"
)

func newToolRegistry(cfg config.Config, slackClient *slack.Client, reminderStore reminder.Store, llmClient llm.Client, secondaryClient llm.Client, secondaryModel string, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy) *registry.Registry {
	tools := registry.NewReadOnlyWithAllowedWrites("luckin-create_order", "luckin-cancel_order", "slack-create_canvas", "tts-speak", "reminder-create", "reminder-cancel")
	tools.Register(reminderTools.CreateTool{Store: reminderStore})
	tools.Register(reminderTools.ListTool{Store: reminderStore})
	tools.Register(reminderTools.CancelTool{Store: reminderStore})
	registerDeferredDiagnosticsTools(tools)
	registerCodeTools(tools, cfg, workspacePolicy, commandPolicy)
	registerIntegrationTools(tools, cfg, commandPolicy)
	registerKnowledgeTools(tools, cfg)
	registerSlackTools(tools, slackClient, cfg)
	registerAgentControlTools(tools, cfg, llmClient, secondaryClient, secondaryModel)
	tools.Register(registry.ToolSearchTool{Registry: tools})
	return tools
}

func registerDeferredDiagnosticsTools(tools *registry.Registry) {
	registerDeferredTools(
		tools,
		registry.CategoryDiagnostics,
		diagnosticsTools.IncidentBriefTool{},
		diagnosticsTools.TimelineTool{},
		diagnosticsTools.EvidenceBoardTool{},
	)
}

func registerCodeTools(tools *registry.Registry, cfg config.Config, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy) {
	intel := codeintel.Manager{Paths: workspacePolicy, Timeout: cfg.Tools.CommandTimeout}
	tools.Register(codeIntelTools.SymbolsTool{Manager: intel})
	tools.Register(codeIntelTools.DefinitionTool{Manager: intel})
	tools.Register(codeIntelTools.ReferencesTool{Manager: intel})
	tools.Register(codeIntelTools.ImplementationTool{Manager: intel})
	tools.Register(codeIntelTools.IncomingCallsTool{Manager: intel})
	tools.Register(codeIntelTools.OutgoingCallsTool{Manager: intel})
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
	tools.Register(shellTools.ReadOnlyTool{
		GCloudPath:     cfg.Tools.GCloudPath,
		KubectlPath:    cfg.Tools.KubectlPath,
		WorkspaceRoots: cfg.Security.WorkspaceRoots,
		Guard:          commandPolicy,
		Timeout:        cfg.Tools.CommandTimeout,
	})

	// K8s native tools: dedicated kubectl wrappers for pods, logs, describe, top,
	// events, rollout, and general get. These provide richer structured output and
	// safer arg handling than the generic shell tool.
	if cfg.Tools.KubectlPath != "" || cfg.Tools.K8sDefaultContext != "" || cfg.Tools.K8sDefaultCluster != "" {
		k8sBase := k8sTools.Base{
			KubectlPath:      cfg.Tools.KubectlPath,
			DefaultContext:   cfg.Tools.K8sDefaultContext,
			DefaultCluster:   cfg.Tools.K8sDefaultCluster,
			DefaultNamespace: cfg.Tools.K8sDefaultNamespace,
			Guard:            commandPolicy,
			Timeout:          cfg.Tools.CommandTimeout,
		}
		registerDeferredTools(
			tools,
			registry.CategoryInfrastructure,
			k8sTools.ContextsTool{Base: k8sBase},
			k8sTools.GetPodsTool{Base: k8sBase},
			k8sTools.LogsTool{Base: k8sBase},
			k8sTools.DescribeTool{Base: k8sBase},
			k8sTools.TopTool{Base: k8sBase},
			k8sTools.EventsTool{Base: k8sBase},
			k8sTools.RolloutTool{Base: k8sBase},
			k8sTools.GetTool{Base: k8sBase},
		)
	}

	registerDeferredTools(
		tools,
		registry.CategoryInfrastructure,
		gcpTools.LogsTool{
			GCloudPath:       cfg.Tools.GCloudPath,
			DefaultProject:   cfg.Tools.GCPDefaultProject,
			DefaultNamespace: cfg.Tools.GCPDefaultNamespace,
			DefaultCluster:   cfg.Tools.GKEDefaultCluster,
			DefaultRegion:    cfg.Tools.GKEDefaultRegion,
			Guard:            commandPolicy,
			Timeout:          cfg.Tools.CommandTimeout,
		},
		gcpTools.RunServicesTool{
			GCloudPath:     cfg.Tools.GCloudPath,
			DefaultProject: cfg.Tools.GCPDefaultProject,
			DefaultRegion:  cfg.Tools.GKEDefaultRegion,
			Guard:          commandPolicy,
			Timeout:        cfg.Tools.CommandTimeout,
		},
		gcpTools.RunRevisionsTool{
			GCloudPath:     cfg.Tools.GCloudPath,
			DefaultProject: cfg.Tools.GCPDefaultProject,
			DefaultRegion:  cfg.Tools.GKEDefaultRegion,
			Guard:          commandPolicy,
			Timeout:        cfg.Tools.CommandTimeout,
		},
		gcpTools.ClustersTool{
			GCloudPath:     cfg.Tools.GCloudPath,
			DefaultProject: cfg.Tools.GCPDefaultProject,
			DefaultRegion:  cfg.Tools.GKEDefaultRegion,
			Guard:          commandPolicy,
			Timeout:        cfg.Tools.CommandTimeout,
		},
	)

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
	registerDeferredTools(
		tools,
		registry.CategoryIntegration,
		githubTools.DispatchWorkflowTool{Client: githubClient},
		githubTools.WorkflowRunsTool{Client: githubClient},
		githubTools.PRDiffTool{Client: githubClient},
		githubTools.PRFileDiffTool{},
		githubTools.JobLogsTool{Client: githubClient},
	)

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
		BraveAPIKey:    cfg.Tools.WebSearchBraveKey,
		BraveBaseURL:   cfg.Tools.WebSearchBraveURL,
	}
	tools.Register(webSearchTools.SearchTool{Client: webClient})
	tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, webSearchTools.ReadPageTool{Client: webClient}))
	tools.Register(knowledgeTools.RunbookSearchTool{})
}

func registerSlackTools(tools *registry.Registry, slackClient *slack.Client, cfg config.Config) {
	tools.Register(slacktool.AskUserTool{Slack: slackClient})
	tools.Register(slacktool.FileSearchTool{Slack: slackClient})
	tools.Register(slacktool.JSONAnalyzeTool{Slack: slackClient})
	registerDeferredTools(tools, registry.CategoryBrowser, slacktool.SendScreenshotTool{Slack: slackClient})
	registerDeferredTools(tools, registry.CategoryIntegration, slacktool.CreateCanvasTool{Slack: slackClient})

	if cfg.Tools.TTSAPIKey != "" {
		tools.Register(ttsTools.SpeakTool{
			Slack:   slackClient,
			APIKey:  cfg.Tools.TTSAPIKey,
			BaseURL: cfg.Tools.TTSBaseURL,
			Model:   cfg.Tools.TTSModel,
		})
	} else {
		tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, ttsTools.SpeakTool{
			Slack:   slackClient,
			APIKey:  cfg.Tools.TTSAPIKey,
			BaseURL: cfg.Tools.TTSBaseURL,
			Model:   cfg.Tools.TTSModel,
		}))
	}
}

func registerDeferredTools(reg *registry.Registry, category string, tools ...registry.Tool) {
	for _, tool := range tools {
		reg.RegisterDeferred(registry.AsDeferred(category, tool))
	}
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
	tools.Register(agentTools.SpillReadTool{})
	tools.Register(delegation.Tool{Manager: delegates})
	tools.Register(delegation.ExploreTool{Manager: delegates})
}

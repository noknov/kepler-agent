package runtime

import (
	"context"

	agentTools "github.com/noknov/slack-copilot-agent/packages/agent"
	"github.com/noknov/slack-copilot-agent/packages/codeintel"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/delegation"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/mcp"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
	"github.com/noknov/slack-copilot-agent/packages/reminder"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/slack"
	codeTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/code"
	codegraphTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/codegraph"
	codeIntelTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/codeintel"
	diagnosticsTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/diagnostics"
	editTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/edit"
	gcpTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/gcp"
	gitTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/git"
	githubTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/github"
	k8sTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/k8s"
	knowledgeTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/knowledge"
	localExecTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/localexec"
	luckinTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/luckin"
	notionTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/notion"
	plannerTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/planner"
	playwrightTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/playwright"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
	reminderTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/reminder"
	shellTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/shell"
	skillTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/skills"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/slacktool"
	ttsTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/tts"
	webSearchTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/websearch"
	youtrackTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/youtrack"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

func NewToolRegistry(cfg config.Config, slackClient *slack.Client, reminderStore reminder.Store, llmClient llm.Client, secondaryClient llm.Client, secondaryModel string, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy, rdb *redisclient.Client, userPrefs userprefs.Store) *registry.Registry {
	return newToolRegistryWithPolicy(
		cfg,
		slackClient,
		reminderStore,
		llmClient,
		secondaryClient,
		secondaryModel,
		workspacePolicy,
		commandPolicy,
		rdb,
		userPrefs,
		policyForSurface(cfg, "slack", slackClient, reminderStore),
	)
}

// NewAppServerToolRegistry builds the transport-neutral tool surface. Slack
// delivery and reminder tools are intentionally absent; transports may add
// their own adapter-specific capabilities around agentcore.
func NewAppServerToolRegistry(cfg config.Config, llmClient llm.Client, secondaryClient llm.Client, secondaryModel string, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy, rdb *redisclient.Client, userPrefs userprefs.Store) *registry.Registry {
	return newToolRegistryWithPolicy(
		cfg,
		nil,
		nil,
		llmClient,
		secondaryClient,
		secondaryModel,
		workspacePolicy,
		commandPolicy,
		rdb,
		userPrefs,
		policyForSurface(cfg, "app-server", nil, nil),
	)
}

func NewCodingToolRegistry(cfg config.Config, llmClient llm.Client, secondaryClient llm.Client, secondaryModel string, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy) *registry.Registry {
	policy := policyForSurface(cfg, "coding", nil, nil)
	// The coding runtime is created only for caller-provided isolated
	// workspaces. Its three mutation tools are part of that surface's contract,
	// while the production Slack surface never receives them.
	for _, name := range []string{"code-write_file", "code-replace", "local-command"} {
		policy.AllowedWriteTools[name] = true
	}
	tools := newToolRegistryWithPolicy(cfg, nil, nil, llmClient, secondaryClient, secondaryModel, workspacePolicy, commandPolicy, nil, nil, policy)
	tools.Register(codingWrite(editTools.WriteFileTool{Paths: workspacePolicy}))
	tools.Register(codingWrite(editTools.ReplaceTool{Paths: workspacePolicy}))
	tools.Register(codingWrite(localExecTools.CommandTool{WorkspaceRoots: workspacePolicy.Roots, Guard: commandPolicy, Timeout: cfg.Tools.CommandTimeout}))
	return tools
}

func policyForSurface(cfg config.Config, surface string, slackClient *slack.Client, reminderStore reminder.Store) registry.CapabilityPolicy {
	integrations := cfg.Integrations
	allowedWrites := make(map[string]bool, len(cfg.Tools.AllowedWriteTools))
	for _, name := range cfg.Tools.AllowedWriteTools {
		if name != "" {
			allowedWrites[name] = true
		}
	}
	return registry.CapabilityPolicy{
		Surface:           surface,
		AllowedWriteTools: allowedWrites,
		AvailableDeps: map[string]bool{
			"github":     integrations.GitHub.Token != "",
			"luckin":     integrations.Luckin.MCPToken != "",
			"notion":     integrations.Notion.Token != "",
			"playwright": integrations.Playwright.MCPURL != "",
			"reminder":   reminderStore != nil,
			"slack":      slackClient != nil,
			"tts":        integrations.TTS.APIKey != "",
			"youtrack":   integrations.YouTrack.URL != "" && integrations.YouTrack.Token != "",
		},
	}
}

func slackExternalWrite(tool registry.Tool, deps ...string) registry.Tool {
	return registry.WithMetadata(tool, registry.ToolMetadata{
		Risk:         registry.RiskExternalWrite,
		Dependencies: append([]string{"slack"}, deps...),
		Surfaces:     []string{"slack"},
	})
}

func codingWrite(tool registry.Tool) registry.Tool {
	return registry.WithMetadata(tool, registry.ToolMetadata{
		Risk:     registry.RiskWrite,
		Surfaces: []string{"coding"},
	})
}

func runtimeRead(tool registry.Tool, deps ...string) registry.Tool {
	return registry.WithMetadata(tool, registry.ToolMetadata{
		Risk:         registry.RiskRead,
		Dependencies: deps,
	})
}

func newToolRegistryWithPolicy(cfg config.Config, slackClient *slack.Client, reminderStore reminder.Store, llmClient llm.Client, secondaryClient llm.Client, secondaryModel string, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy, rdb *redisclient.Client, userPrefs userprefs.Store, policy registry.CapabilityPolicy) *registry.Registry {
	tools := registry.NewWithPolicy(policy)
	tools.Register(slackExternalWrite(reminderTools.CreateTool{
		Store: reminderStore,
		OnCreate: func(ctx context.Context) {
			if rdb != nil {
				_ = rdb.Publish(ctx, "reminders:new", "1")
			}
		},
	}, "reminder"))
	tools.Register(runtimeRead(reminderTools.ListTool{Store: reminderStore}, "reminder"))
	tools.Register(slackExternalWrite(reminderTools.CancelTool{Store: reminderStore}, "reminder"))
	registerDeferredDiagnosticsTools(tools)
	registerCodeTools(tools, cfg, workspacePolicy, commandPolicy)
	registerIntegrationTools(tools, cfg, commandPolicy)
	registerKnowledgeTools(tools, cfg)
	if slackClient != nil {
		registerSlackTools(tools, slackClient, cfg)
	}
	registerAgentControlTools(tools, cfg, llmClient, secondaryClient, secondaryModel, userPrefs)
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
	codegraphBase := codegraphTools.Base{Paths: workspacePolicy, Timeout: cfg.Tools.CommandTimeout}
	registerDeferredTools(
		tools,
		registry.CategoryCode,
		codegraphTools.OverviewTool{Base: codegraphBase},
		codegraphTools.DependenciesTool{Base: codegraphBase},
		codegraphTools.SymbolsTool{Base: codegraphBase},
		codegraphTools.DefinitionTool{Base: codegraphBase},
		codegraphTools.ReferencesTool{Base: codegraphBase},
		codegraphTools.ImplementationsTool{Base: codegraphBase},
		codegraphTools.CallersTool{Base: codegraphBase},
		codegraphTools.CalleesTool{Base: codegraphBase},
		codegraphTools.CallgraphTool{Base: codegraphBase},
		codegraphTools.ImpactTool{Base: codegraphBase},
	)
}

func registerIntegrationTools(tools *registry.Registry, cfg config.Config, commandPolicy safety.CommandPolicy) {
	integrations := cfg.Integrations
	tools.Register(shellTools.ReadOnlyTool{
		GCloudPath:     integrations.GCP.GCloudPath,
		KubectlPath:    integrations.K8s.KubectlPath,
		WorkspaceRoots: cfg.Security.WorkspaceRoots,
		Guard:          commandPolicy,
		Timeout:        cfg.Tools.CommandTimeout,
	})

	// K8s native tools: dedicated kubectl wrappers for pods, logs, describe, top,
	// events, rollout, and general get. These provide richer structured output and
	// safer arg handling than the generic shell tool.
	if integrations.K8s.KubectlPath != "" || integrations.K8s.DefaultContext != "" || integrations.K8s.DefaultCluster != "" {
		k8sBase := k8sTools.Base{
			KubectlPath:      integrations.K8s.KubectlPath,
			DefaultContext:   integrations.K8s.DefaultContext,
			DefaultCluster:   integrations.K8s.DefaultCluster,
			DefaultNamespace: integrations.K8s.DefaultNamespace,
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
			GCloudPath:       integrations.GCP.GCloudPath,
			DefaultProject:   integrations.GCP.DefaultProject,
			DefaultNamespace: integrations.GCP.DefaultNamespace,
			DefaultCluster:   integrations.GCP.DefaultCluster,
			DefaultRegion:    integrations.GCP.DefaultRegion,
			Guard:            commandPolicy,
			Timeout:          cfg.Tools.CommandTimeout,
		},
		gcpTools.RunServicesTool{
			GCloudPath:     integrations.GCP.GCloudPath,
			DefaultProject: integrations.GCP.DefaultProject,
			DefaultRegion:  integrations.GCP.DefaultRegion,
			Guard:          commandPolicy,
			Timeout:        cfg.Tools.CommandTimeout,
		},
		gcpTools.RunRevisionsTool{
			GCloudPath:     integrations.GCP.GCloudPath,
			DefaultProject: integrations.GCP.DefaultProject,
			DefaultRegion:  integrations.GCP.DefaultRegion,
			Guard:          commandPolicy,
			Timeout:        cfg.Tools.CommandTimeout,
		},
		gcpTools.ClustersTool{
			GCloudPath:     integrations.GCP.GCloudPath,
			DefaultProject: integrations.GCP.DefaultProject,
			DefaultRegion:  integrations.GCP.DefaultRegion,
			Guard:          commandPolicy,
			Timeout:        cfg.Tools.CommandTimeout,
		},
	)

	notionClient := notionTools.Client{
		Token:         integrations.Notion.Token,
		DatabaseID:    integrations.Notion.DatabaseID,
		TitleProperty: integrations.Notion.TitleProperty,
		Version:       integrations.Notion.Version,
	}
	if notionClient.Token != "" {
		tools.Register(runtimeRead(notionTools.SearchTool{Client: notionClient}, "notion"))
		tools.Register(runtimeRead(notionTools.GetPageTool{Client: notionClient}, "notion"))
		tools.Register(runtimeRead(notionTools.QueryDatabaseTool{Client: notionClient}, "notion"))
	} else {
		tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, runtimeRead(notionTools.SearchTool{Client: notionClient}, "notion")))
		tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, runtimeRead(notionTools.GetPageTool{Client: notionClient}, "notion")))
		tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, runtimeRead(notionTools.QueryDatabaseTool{Client: notionClient}, "notion")))
	}

	youtrackClient := youtrackTools.Client{BaseURL: integrations.YouTrack.URL, Token: integrations.YouTrack.Token}
	tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, runtimeRead(youtrackTools.GetIssueTool{Client: youtrackClient}, "youtrack")))
	tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, runtimeRead(youtrackTools.SearchTool{Client: youtrackClient}, "youtrack")))

	githubClient := githubTools.Client{
		Token:      integrations.GitHub.Token,
		APIBaseURL: integrations.GitHub.APIBaseURL,
		Owner:      integrations.GitHub.DefaultOwner,
		Repo:       integrations.GitHub.DefaultRepo,
	}
	registerDeferredTools(
		tools,
		registry.CategoryIntegration,
		slackExternalWrite(githubTools.DispatchWorkflowTool{Client: githubClient}),
		githubTools.WorkflowRunsTool{Client: githubClient},
		githubTools.PRDiffTool{Client: githubClient},
		githubTools.PRFileDiffTool{Client: githubClient},
		githubTools.JobLogsTool{Client: githubClient},
	)

	luckinTools.RegisterDeferredAll(tools, &luckinTools.Client{
		MCP: &mcp.Client{
			ServiceName: "luckin",
			URL:         integrations.Luckin.MCPURL,
			Token:       integrations.Luckin.MCPToken,
		},
	}, registry.CategoryIntegration)

	playwrightTools.RegisterDeferredAll(tools, &playwrightTools.Client{
		MCP: &mcp.Client{
			ServiceName: "playwright",
			URL:         integrations.Playwright.MCPURL,
			Token:       integrations.Playwright.MCPToken,
		},
	}, registry.CategoryBrowser)
}

func registerKnowledgeTools(tools *registry.Registry, cfg config.Config) {
	webSearch := cfg.Integrations.WebSearch
	webClient := webSearchTools.Client{
		Provider:       webSearch.Provider,
		GoogleAPIKey:   webSearch.GoogleKey,
		GoogleCX:       webSearch.GoogleCX,
		SerpAPIKey:     webSearch.SerpAPIKey,
		SerpAPIBaseURL: webSearch.SerpAPIURL,
		SearXNGBaseURL: webSearch.SearXNGURL,
		BraveAPIKey:    webSearch.BraveKey,
		BraveBaseURL:   webSearch.BraveURL,
	}
	tools.Register(webSearchTools.SearchTool{Client: webClient})
	tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, webSearchTools.ReadPageTool{Client: webClient}))
	tools.Register(knowledgeTools.RunbookSearchTool{})
}

func registerSlackTools(tools *registry.Registry, slackClient *slack.Client, cfg config.Config) {
	tts := cfg.Integrations.TTS
	tools.Register(runtimeRead(slacktool.AskUserTool{Slack: slackClient}, "slack"))
	tools.Register(runtimeRead(slacktool.FileSearchTool{Slack: slackClient}, "slack"))
	tools.Register(runtimeRead(slacktool.JSONAnalyzeTool{Slack: slackClient}, "slack"))
	registerDeferredTools(tools, registry.CategoryBrowser, slackExternalWrite(slacktool.SendScreenshotTool{Slack: slackClient}))
	registerDeferredTools(tools, registry.CategoryIntegration, slackExternalWrite(slacktool.CreateCanvasTool{Slack: slackClient}))

	if tts.APIKey != "" {
		tools.Register(slackExternalWrite(ttsTools.SpeakTool{
			Slack:   slackClient,
			APIKey:  tts.APIKey,
			BaseURL: tts.BaseURL,
			Model:   tts.Model,
		}, "tts"))
	} else {
		tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, slackExternalWrite(ttsTools.SpeakTool{
			Slack:   slackClient,
			APIKey:  tts.APIKey,
			BaseURL: tts.BaseURL,
			Model:   tts.Model,
		}, "tts")))
	}
}

func registerDeferredTools(reg *registry.Registry, category string, tools ...registry.Tool) {
	for _, tool := range tools {
		reg.RegisterDeferred(registry.AsDeferred(category, tool))
	}
}

func registerAgentControlTools(tools *registry.Registry, cfg config.Config, llmClient llm.Client, secondaryClient llm.Client, secondaryModel string, userPrefs userprefs.Store) {
	delegates := delegation.NewManager(llmClient, cfg.LLM.Model, cfg.LLM.Thinking)
	if secondaryClient != nil && secondaryModel != "" {
		delegates.SetSecondaryClient(secondaryClient, secondaryModel)
	}
	delegates.SetPolicyPrompt(prompts.RulesAndSkillsPrompt())
	delegates.SetTools(tools)
	tools.Register(plannerTools.PlanTool{})
	tools.Register(skillTools.LoadTool{UserPrefs: userPrefs})
	tools.Register(agentTools.SpillReadTool{})
	tools.Register(delegation.Tool{Manager: delegates})
	tools.Register(delegation.ExploreTool{Manager: delegates})
}

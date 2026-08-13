package hostedtools

import (
	"github.com/noknov/slack-copilot-agent/packages/codeintel"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/mcp"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	codeTools "github.com/noknov/slack-copilot-agent/packages/tools/code"
	codegraphTools "github.com/noknov/slack-copilot-agent/packages/tools/codegraph"
	codeIntelTools "github.com/noknov/slack-copilot-agent/packages/tools/codeintel"
	diagnosticsTools "github.com/noknov/slack-copilot-agent/packages/tools/diagnostics"
	gcpTools "github.com/noknov/slack-copilot-agent/packages/tools/gcp"
	gitTools "github.com/noknov/slack-copilot-agent/packages/tools/git"
	githubTools "github.com/noknov/slack-copilot-agent/packages/tools/github"
	k8sTools "github.com/noknov/slack-copilot-agent/packages/tools/k8s"
	knowledgeTools "github.com/noknov/slack-copilot-agent/packages/tools/knowledge"
	luckinTools "github.com/noknov/slack-copilot-agent/packages/tools/luckin"
	notionTools "github.com/noknov/slack-copilot-agent/packages/tools/notion"
	plannerTools "github.com/noknov/slack-copilot-agent/packages/tools/planner"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
	skillTools "github.com/noknov/slack-copilot-agent/packages/tools/skills"
	webSearchTools "github.com/noknov/slack-copilot-agent/packages/tools/websearch"
	youtrackTools "github.com/noknov/slack-copilot-agent/packages/tools/youtrack"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

type SurfaceOptions struct {
	Name          string
	AvailableDeps map[string]bool
}

func NewCatalog(cfg config.Config, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy, userPrefs userprefs.Store, surface SurfaceOptions) *registry.Registry {
	return newToolRegistryWithPolicy(
		cfg,
		workspacePolicy,
		commandPolicy,
		userPrefs,
		policyForSurface(cfg, surface),
	)
}

func policyForSurface(cfg config.Config, surface SurfaceOptions) registry.CapabilityPolicy {
	integrations := cfg.Integrations
	availableDeps := map[string]bool{
		"github":   integrations.GitHub.Token != "",
		"luckin":   integrations.Luckin.MCPToken != "",
		"notion":   integrations.Notion.Token != "",
		"tts":      integrations.TTS.APIKey != "",
		"youtrack": integrations.YouTrack.URL != "" && integrations.YouTrack.Token != "",
	}
	for name, available := range surface.AvailableDeps {
		availableDeps[name] = available
	}
	return registry.CapabilityPolicy{
		Surface:       surface.Name,
		AvailableDeps: availableDeps,
	}
}

func runtimeRead(tool registry.Tool, deps ...string) registry.Tool {
	return registry.WithMetadata(tool, registry.ToolMetadata{
		Risk:         registry.RiskRead,
		Dependencies: deps,
		Network:      true,
	})
}

func networkTool(tool registry.Tool, deps ...string) registry.Tool {
	return registry.WithMetadata(tool, registry.ToolMetadata{Dependencies: deps, Network: true})
}

func newToolRegistryWithPolicy(cfg config.Config, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy, userPrefs userprefs.Store, policy registry.CapabilityPolicy) *registry.Registry {
	tools := registry.NewWithPolicy(policy)
	registerDeferredDiagnosticsTools(tools)
	registerCodeTools(tools, cfg, workspacePolicy, commandPolicy)
	registerIntegrationTools(tools, cfg, commandPolicy)
	registerKnowledgeTools(tools, cfg)
	registerAgentControlTools(tools, userPrefs)
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
	tools.Register(runtimeRead(gitTools.RepoSearchTool{Base: gitBase}))
	tools.Register(runtimeRead(gitTools.RepoReadFileTool{Base: gitBase}))
	tools.Register(runtimeRead(gitTools.FetchRefTool{Base: gitBase}))
	tools.Register(runtimeRead(gitTools.SearchRefTool{Base: gitBase}))
	tools.Register(runtimeRead(gitTools.ReadFileRefTool{Base: gitBase}))
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
			runtimeRead(k8sTools.GetPodsTool{Base: k8sBase}),
			runtimeRead(k8sTools.LogsTool{Base: k8sBase}),
			runtimeRead(k8sTools.DescribeTool{Base: k8sBase}),
			runtimeRead(k8sTools.TopTool{Base: k8sBase}),
			runtimeRead(k8sTools.EventsTool{Base: k8sBase}),
			runtimeRead(k8sTools.RolloutTool{Base: k8sBase}),
			runtimeRead(k8sTools.GetTool{Base: k8sBase}),
		)
	}

	registerDeferredTools(
		tools,
		registry.CategoryInfrastructure,
		runtimeRead(gcpTools.LogsTool{
			GCloudPath:       integrations.GCP.GCloudPath,
			DefaultProject:   integrations.GCP.DefaultProject,
			DefaultNamespace: integrations.GCP.DefaultNamespace,
			DefaultCluster:   integrations.GCP.DefaultCluster,
			DefaultRegion:    integrations.GCP.DefaultRegion,
			Guard:            commandPolicy,
			Timeout:          cfg.Tools.CommandTimeout,
		}),
		runtimeRead(gcpTools.RunServicesTool{
			GCloudPath:     integrations.GCP.GCloudPath,
			DefaultProject: integrations.GCP.DefaultProject,
			DefaultRegion:  integrations.GCP.DefaultRegion,
			Guard:          commandPolicy,
			Timeout:        cfg.Tools.CommandTimeout,
		}),
		runtimeRead(gcpTools.RunRevisionsTool{
			GCloudPath:     integrations.GCP.GCloudPath,
			DefaultProject: integrations.GCP.DefaultProject,
			DefaultRegion:  integrations.GCP.DefaultRegion,
			Guard:          commandPolicy,
			Timeout:        cfg.Tools.CommandTimeout,
		}),
		runtimeRead(gcpTools.ClustersTool{
			GCloudPath:     integrations.GCP.GCloudPath,
			DefaultProject: integrations.GCP.DefaultProject,
			DefaultRegion:  integrations.GCP.DefaultRegion,
			Guard:          commandPolicy,
			Timeout:        cfg.Tools.CommandTimeout,
		}),
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
		networkTool(githubTools.DispatchWorkflowTool{Client: githubClient}, "github"),
		networkTool(githubTools.WorkflowRunsTool{Client: githubClient}, "github"),
		networkTool(githubTools.PRDiffTool{Client: githubClient}, "github"),
		networkTool(githubTools.PRFileDiffTool{Client: githubClient}, "github"),
		networkTool(githubTools.JobLogsTool{Client: githubClient}, "github"),
	)

	luckinTools.RegisterDeferredAll(tools, &luckinTools.Client{
		MCP: &mcp.Client{
			ServiceName: "luckin",
			URL:         integrations.Luckin.MCPURL,
			Token:       integrations.Luckin.MCPToken,
		},
	}, registry.CategoryIntegration)

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
	tools.Register(runtimeRead(webSearchTools.SearchTool{Client: webClient}))
	tools.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, runtimeRead(webSearchTools.ReadPageTool{Client: webClient})))
	tools.Register(knowledgeTools.RunbookSearchTool{})
}

func registerDeferredTools(reg *registry.Registry, category string, tools ...registry.Tool) {
	for _, tool := range tools {
		reg.RegisterDeferred(registry.AsDeferred(category, tool))
	}
}

func registerAgentControlTools(tools *registry.Registry, userPrefs userprefs.Store) {
	tools.Register(plannerTools.PlanTool{})
	tools.Register(skillTools.LoadTool{UserPrefs: userPrefs})
}

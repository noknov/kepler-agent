package hostedtools

import (
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
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
	skillTools "github.com/noknov/slack-copilot-agent/packages/tools/skills"
	webSearchTools "github.com/noknov/slack-copilot-agent/packages/tools/websearch"
	workspaceTools "github.com/noknov/slack-copilot-agent/packages/tools/workspace"
	youtrackTools "github.com/noknov/slack-copilot-agent/packages/tools/youtrack"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

type SurfaceOptions struct {
	Name          string
	AvailableDeps map[string]bool
}

func NewCatalog(cfg config.Config, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy, userPrefs userprefs.Store, surface SurfaceOptions) (*tool.Catalog, error) {
	policy := policyForSurface(cfg, surface)
	catalog, err := tool.NewCatalog()
	if err != nil {
		return nil, err
	}
	registerDeferredDiagnosticsTools(catalog, policy)
	registerWorkspaceTools(catalog, policy, workspacePolicy)
	registerCodeTools(catalog, policy, cfg, workspacePolicy, commandPolicy)
	registerIntegrationTools(catalog, policy, cfg, commandPolicy)
	registerKnowledgeTools(catalog, policy, cfg)
	registerAgentControlTools(catalog, policy, userPrefs)
	if err := catalog.Register(tool.NewSearchTool(catalog)); err != nil {
		return nil, err
	}
	return catalog, nil
}

func PolicyForSurface(cfg config.Config, surface SurfaceOptions) tool.SurfacePolicy {
	return policyForSurface(cfg, surface)
}

func policyForSurface(cfg config.Config, surface SurfaceOptions) tool.SurfacePolicy {
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
	return tool.SurfacePolicy{
		Surface:       surface.Name,
		AvailableDeps: availableDeps,
	}
}

func readTool(item tool.Tool, deps ...string) tool.Tool {
	return tool.Annotate(item, tool.Descriptor{Effects: []tool.Effect{tool.EffectRead, tool.EffectNetwork}, Parallel: true, Dependencies: deps})
}

func networkTool(item tool.Tool, deps ...string) tool.Tool {
	return tool.Annotate(item, tool.Descriptor{Effects: []tool.Effect{tool.EffectNetwork}, Dependencies: deps})
}

func writeTool(item tool.Tool, deps ...string) tool.Tool {
	return tool.Annotate(item, tool.Descriptor{Effects: []tool.Effect{tool.EffectWorkspaceWrite}, Dependencies: deps})
}

func externalWrite(item tool.Tool, deps ...string) tool.Tool {
	return tool.Annotate(item, tool.Descriptor{Effects: []tool.Effect{tool.EffectExternalWrite, tool.EffectNetwork}, Dependencies: deps, Surfaces: []string{"slack"}})
}

func registerDeferredDiagnosticsTools(catalog *tool.Catalog, policy tool.SurfacePolicy) {
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryDiagnostics, diagnosticsTools.IncidentBriefTool{})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryDiagnostics, diagnosticsTools.TimelineTool{})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryDiagnostics, diagnosticsTools.EvidenceBoardTool{})
}

func registerWorkspaceTools(catalog *tool.Catalog, policy tool.SurfacePolicy, workspacePolicy safety.WorkspacePolicy) {
	if len(workspacePolicy.Roots) == 0 {
		return
	}
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryWorkspace, workspaceTools.ListReposTool{Roots: workspacePolicy.Roots})
}

func registerCodeTools(catalog *tool.Catalog, policy tool.SurfacePolicy, cfg config.Config, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy) {
	intel := codeintel.Manager{Paths: workspacePolicy, Timeout: cfg.Tools.CommandTimeout}
	_ = catalog.RegisterVisible(policy, codeIntelTools.SymbolsTool{Manager: intel})
	_ = catalog.RegisterVisible(policy, codeIntelTools.DefinitionTool{Manager: intel})
	_ = catalog.RegisterVisible(policy, codeIntelTools.ReferencesTool{Manager: intel})
	_ = catalog.RegisterVisible(policy, codeIntelTools.ImplementationTool{Manager: intel})
	_ = catalog.RegisterVisible(policy, codeIntelTools.IncomingCallsTool{Manager: intel})
	_ = catalog.RegisterVisible(policy, codeIntelTools.OutgoingCallsTool{Manager: intel})
	_ = catalog.RegisterVisible(policy, codeIntelTools.DiagnosticsTool{Manager: intel})
	_ = catalog.RegisterVisible(policy, codeTools.SearchTool{Paths: workspacePolicy})
	_ = catalog.RegisterVisible(policy, codeTools.ReadFileTool{Paths: workspacePolicy})

	gitBase := gitTools.Base{Paths: workspacePolicy, Guard: commandPolicy, Timeout: cfg.Tools.CommandTimeout}
	_ = catalog.RegisterVisible(policy, readTool(gitTools.RepoSearchTool{Base: gitBase}))
	_ = catalog.RegisterVisible(policy, readTool(gitTools.RepoReadFileTool{Base: gitBase}))
	_ = catalog.RegisterVisible(policy, readTool(gitTools.FetchRefTool{Base: gitBase}))
	_ = catalog.RegisterVisible(policy, readTool(gitTools.SearchRefTool{Base: gitBase}))
	_ = catalog.RegisterVisible(policy, readTool(gitTools.ReadFileRefTool{Base: gitBase}))
	_ = catalog.RegisterVisible(policy, gitTools.StatusTool{Base: gitBase})
	_ = catalog.RegisterVisible(policy, gitTools.LogTool{Base: gitBase})
	_ = catalog.RegisterVisible(policy, gitTools.ShowTool{Base: gitBase})

	codegraphBase := codegraphTools.Base{Paths: workspacePolicy, Timeout: cfg.Tools.CommandTimeout}
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryCode, codegraphTools.OverviewTool{Base: codegraphBase})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryCode, codegraphTools.DependenciesTool{Base: codegraphBase})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryCode, codegraphTools.SymbolsTool{Base: codegraphBase})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryCode, codegraphTools.DefinitionTool{Base: codegraphBase})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryCode, codegraphTools.ReferencesTool{Base: codegraphBase})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryCode, codegraphTools.ImplementationsTool{Base: codegraphBase})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryCode, codegraphTools.CallersTool{Base: codegraphBase})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryCode, codegraphTools.CalleesTool{Base: codegraphBase})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryCode, codegraphTools.CallgraphTool{Base: codegraphBase})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryCode, codegraphTools.ImpactTool{Base: codegraphBase})
}

func registerIntegrationTools(catalog *tool.Catalog, policy tool.SurfacePolicy, cfg config.Config, commandPolicy safety.CommandPolicy) {
	integrations := cfg.Integrations
	if integrations.K8s.KubectlPath != "" || integrations.K8s.DefaultContext != "" || integrations.K8s.DefaultCluster != "" {
		k8sBase := k8sTools.Base{
			KubectlPath:      integrations.K8s.KubectlPath,
			DefaultContext:   integrations.K8s.DefaultContext,
			DefaultCluster:   integrations.K8s.DefaultCluster,
			DefaultNamespace: integrations.K8s.DefaultNamespace,
			Guard:            commandPolicy,
			Timeout:          cfg.Tools.CommandTimeout,
		}
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, k8sTools.ContextsTool{Base: k8sBase})
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, readTool(k8sTools.GetPodsTool{Base: k8sBase}))
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, readTool(k8sTools.LogsTool{Base: k8sBase}))
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, readTool(k8sTools.DescribeTool{Base: k8sBase}))
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, readTool(k8sTools.TopTool{Base: k8sBase}))
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, readTool(k8sTools.EventsTool{Base: k8sBase}))
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, readTool(k8sTools.RolloutTool{Base: k8sBase}))
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, readTool(k8sTools.GetTool{Base: k8sBase}))
	}

	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, readTool(gcpTools.LogsTool{
		GCloudPath:       integrations.GCP.GCloudPath,
		DefaultProject:   integrations.GCP.DefaultProject,
		DefaultNamespace: integrations.GCP.DefaultNamespace,
		DefaultCluster:   integrations.GCP.DefaultCluster,
		DefaultRegion:    integrations.GCP.DefaultRegion,
		Guard:            commandPolicy,
		Timeout:          cfg.Tools.CommandTimeout,
	}))
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, readTool(gcpTools.RunServicesTool{
		GCloudPath:     integrations.GCP.GCloudPath,
		DefaultProject: integrations.GCP.DefaultProject,
		DefaultRegion:  integrations.GCP.DefaultRegion,
		Guard:          commandPolicy,
		Timeout:        cfg.Tools.CommandTimeout,
	}))
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, readTool(gcpTools.RunRevisionsTool{
		GCloudPath:     integrations.GCP.GCloudPath,
		DefaultProject: integrations.GCP.DefaultProject,
		DefaultRegion:  integrations.GCP.DefaultRegion,
		Guard:          commandPolicy,
		Timeout:        cfg.Tools.CommandTimeout,
	}))
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, readTool(gcpTools.ClustersTool{
		GCloudPath:     integrations.GCP.GCloudPath,
		DefaultProject: integrations.GCP.DefaultProject,
		DefaultRegion:  integrations.GCP.DefaultRegion,
		Guard:          commandPolicy,
		Timeout:        cfg.Tools.CommandTimeout,
	}))

	notionClient := notionTools.Client{
		Token:         integrations.Notion.Token,
		DatabaseID:    integrations.Notion.DatabaseID,
		TitleProperty: integrations.Notion.TitleProperty,
		Version:       integrations.Notion.Version,
	}
	if notionClient.Token != "" {
		_ = catalog.RegisterVisible(policy, readTool(notionTools.SearchTool{Client: notionClient}, "notion"))
		_ = catalog.RegisterVisible(policy, readTool(notionTools.GetPageTool{Client: notionClient}, "notion"))
		_ = catalog.RegisterVisible(policy, readTool(notionTools.QueryDatabaseTool{Client: notionClient}, "notion"))
	} else {
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, readTool(notionTools.SearchTool{Client: notionClient}, "notion"))
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, readTool(notionTools.GetPageTool{Client: notionClient}, "notion"))
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, readTool(notionTools.QueryDatabaseTool{Client: notionClient}, "notion"))
	}

	youtrackClient := youtrackTools.Client{BaseURL: integrations.YouTrack.URL, Token: integrations.YouTrack.Token}
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, readTool(youtrackTools.GetIssueTool{Client: youtrackClient}, "youtrack"))
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, readTool(youtrackTools.SearchTool{Client: youtrackClient}, "youtrack"))

	githubClient := githubTools.Client{
		Token:      integrations.GitHub.Token,
		APIBaseURL: integrations.GitHub.APIBaseURL,
		Owner:      integrations.GitHub.DefaultOwner,
		Repo:       integrations.GitHub.DefaultRepo,
	}
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, networkTool(githubTools.DispatchWorkflowTool{Client: githubClient}, "github"))
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, networkTool(githubTools.WorkflowRunsTool{Client: githubClient}, "github"))
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, networkTool(githubTools.PRDiffTool{Client: githubClient}, "github"))
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, networkTool(githubTools.PRFileDiffTool{Client: githubClient}, "github"))
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, networkTool(githubTools.JobLogsTool{Client: githubClient}, "github"))

	luckinClient := &luckinTools.Client{
		MCP: &mcp.Client{
			ServiceName: "luckin",
			URL:         integrations.Luckin.MCPURL,
			Token:       integrations.Luckin.MCPToken,
		},
	}
	for _, item := range luckinTools.Tools(luckinClient) {
		annotated := luckinTools.Annotate(item)
		if integrations.Luckin.MCPToken != "" {
			_ = catalog.RegisterVisible(policy, annotated)
		} else {
			_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, annotated)
		}
	}
}

func registerKnowledgeTools(catalog *tool.Catalog, policy tool.SurfacePolicy, cfg config.Config) {
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
	_ = catalog.RegisterVisible(policy, readTool(webSearchTools.SearchTool{Client: webClient}))
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, readTool(webSearchTools.ReadPageTool{Client: webClient}))
	_ = catalog.RegisterVisible(policy, knowledgeTools.RunbookSearchTool{})
}

func registerAgentControlTools(catalog *tool.Catalog, policy tool.SurfacePolicy, userPrefs userprefs.Store) {
	_ = catalog.RegisterVisible(policy, plannerTools.PlanTool{})
	_ = catalog.RegisterVisible(policy, skillTools.LoadTool{UserPrefs: userPrefs})
}

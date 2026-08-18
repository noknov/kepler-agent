package hostedtools

import (
	"context"
	"fmt"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/codeintel"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/connections"
	"github.com/noknov/slack-copilot-agent/packages/mcp"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	codeTools "github.com/noknov/slack-copilot-agent/packages/tools/code"
	codegraphTools "github.com/noknov/slack-copilot-agent/packages/tools/codegraph"
	codeIntelTools "github.com/noknov/slack-copilot-agent/packages/tools/codeintel"
	clickstackTools "github.com/noknov/slack-copilot-agent/packages/tools/clickstack"
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
	Connections   *connections.Service
}

type CatalogBundle struct {
	Catalog    *tool.Catalog
	ClickStack *clickstackTools.Registrar
}

func NewCatalog(cfg config.Config, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy, userPrefs userprefs.Store, surface SurfaceOptions) (CatalogBundle, error) {
	policy := policyForSurface(cfg, surface)
	catalog, err := tool.NewCatalog()
	if err != nil {
		return CatalogBundle{}, err
	}
	registerDeferredDiagnosticsTools(catalog, policy)
	registerWorkspaceTools(catalog, policy, workspacePolicy)
	registerCodeTools(catalog, policy, cfg, workspacePolicy, commandPolicy)
	registerIntegrationTools(catalog, policy, cfg, commandPolicy, surface.Connections)
	clickstackReg := clickstackTools.NewRegistrar(cfg.Integrations.ClickStack, surface.Connections)
	if clickstackReg != nil {
		if err := clickstackReg.Ensure(context.Background(), catalog, policy, ""); err != nil {
			return CatalogBundle{}, fmt.Errorf("register ClickStack MCP tools: %w", err)
		}
	}
	registerKnowledgeTools(catalog, policy, cfg)
	registerAgentControlTools(catalog, policy, userPrefs)
	if err := catalog.Register(tool.NewSearchTool(catalog)); err != nil {
		return CatalogBundle{}, err
	}
	return CatalogBundle{Catalog: catalog, ClickStack: clickstackReg}, nil
}

func PolicyForSurface(cfg config.Config, surface SurfaceOptions) tool.SurfacePolicy {
	return policyForSurface(cfg, surface)
}

func policyForSurface(cfg config.Config, surface SurfaceOptions) tool.SurfacePolicy {
	integrations := cfg.Integrations
	availableDeps := map[string]bool{
		"github":     integrations.GitHub.Token != "" || cfg.Connections.GitHubOAuthEnabled(),
		"luckin":     integrations.Luckin.MCPToken != "",
		"clickstack": integrations.ClickStack.Enabled(),
		"notion":     integrations.Notion.Token != "",
		"tts":      integrations.TTS.APIKey != "",
		"youtrack": integrations.YouTrack.URL != "" && integrations.YouTrack.Token != "",
	}
	for name, available := range surface.AvailableDeps {
		availableDeps[name] = available
	}
	if surface.Connections != nil && surface.Connections.Config.SlackEnabled() {
		availableDeps["slack-connection"] = true
	}
	if surface.Connections != nil && surface.Connections.Config.ClickStackEnabled() {
		availableDeps["clickstack-connection"] = true
	}
	return tool.SurfacePolicy{
		Surface:       surface.Name,
		AvailableDeps: availableDeps,
	}
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
	_ = catalog.RegisterVisible(policy, gitTools.RepoSearchTool{Base: gitBase})
	_ = catalog.RegisterVisible(policy, gitTools.RepoReadFileTool{Base: gitBase})
	_ = catalog.RegisterVisible(policy, gitTools.FetchRefTool{Base: gitBase})
	_ = catalog.RegisterVisible(policy, gitTools.SearchRefTool{Base: gitBase})
	_ = catalog.RegisterVisible(policy, gitTools.ReadFileRefTool{Base: gitBase})
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

func registerIntegrationTools(catalog *tool.Catalog, policy tool.SurfacePolicy, cfg config.Config, commandPolicy safety.CommandPolicy, conn *connections.Service) {
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
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, k8sTools.GetPodsTool{Base: k8sBase})
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, k8sTools.LogsTool{Base: k8sBase})
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, k8sTools.DescribeTool{Base: k8sBase})
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, k8sTools.TopTool{Base: k8sBase})
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, k8sTools.EventsTool{Base: k8sBase})
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, k8sTools.RolloutTool{Base: k8sBase})
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, k8sTools.GetTool{Base: k8sBase})
	}

	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, gcpTools.LogsTool{
		GCloudPath:       integrations.GCP.GCloudPath,
		DefaultProject:   integrations.GCP.DefaultProject,
		DefaultNamespace: integrations.GCP.DefaultNamespace,
		DefaultCluster:   integrations.GCP.DefaultCluster,
		DefaultRegion:    integrations.GCP.DefaultRegion,
		Guard:            commandPolicy,
		Timeout:          cfg.Tools.CommandTimeout,
	})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, gcpTools.RunServicesTool{
		GCloudPath:     integrations.GCP.GCloudPath,
		DefaultProject: integrations.GCP.DefaultProject,
		DefaultRegion:  integrations.GCP.DefaultRegion,
		Guard:          commandPolicy,
		Timeout:        cfg.Tools.CommandTimeout,
	})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, gcpTools.RunRevisionsTool{
		GCloudPath:     integrations.GCP.GCloudPath,
		DefaultProject: integrations.GCP.DefaultProject,
		DefaultRegion:  integrations.GCP.DefaultRegion,
		Guard:          commandPolicy,
		Timeout:        cfg.Tools.CommandTimeout,
	})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, gcpTools.ClustersTool{
		GCloudPath:     integrations.GCP.GCloudPath,
		DefaultProject: integrations.GCP.DefaultProject,
		DefaultRegion:  integrations.GCP.DefaultRegion,
		Guard:          commandPolicy,
		Timeout:        cfg.Tools.CommandTimeout,
	})

	notionClient := notionTools.Client{
		Token:         integrations.Notion.Token,
		DatabaseID:    integrations.Notion.DatabaseID,
		TitleProperty: integrations.Notion.TitleProperty,
		Version:       integrations.Notion.Version,
	}
	if notionClient.Token != "" {
		_ = catalog.RegisterVisible(policy, notionTools.SearchTool{Client: notionClient})
		_ = catalog.RegisterVisible(policy, notionTools.GetPageTool{Client: notionClient})
		_ = catalog.RegisterVisible(policy, notionTools.QueryDatabaseTool{Client: notionClient})
	} else {
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, notionTools.SearchTool{Client: notionClient})
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, notionTools.GetPageTool{Client: notionClient})
		_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, notionTools.QueryDatabaseTool{Client: notionClient})
	}

	youtrackClient := youtrackTools.Client{BaseURL: integrations.YouTrack.URL, Token: integrations.YouTrack.Token}
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, youtrackTools.GetIssueTool{Client: youtrackClient})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, youtrackTools.SearchTool{Client: youtrackClient})

	githubClient := githubTools.Client{
		Token:      integrations.GitHub.Token,
		APIBaseURL: integrations.GitHub.APIBaseURL,
		Owner:      integrations.GitHub.DefaultOwner,
		Repo:       integrations.GitHub.DefaultRepo,
	}
	var githubSource githubTools.ClientSource
	if conn != nil && conn.Config.GitHubEnabled() {
		githubSource = githubTools.ConnectedSource{Service: *conn, Defaults: githubClient}
	}
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, githubTools.DispatchWorkflowTool{Source: githubSource, Client: githubClient})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, githubTools.WorkflowRunsTool{Source: githubSource, Client: githubClient})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, githubTools.PRDiffTool{Source: githubSource, Client: githubClient})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, githubTools.PRFileDiffTool{Source: githubSource, Client: githubClient})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, githubTools.JobLogsTool{Source: githubSource, Client: githubClient})

	luckinClient := &luckinTools.Client{
		MCP: &mcp.Client{
			ServiceName: "luckin",
			URL:         integrations.Luckin.MCPURL,
			Token:       integrations.Luckin.MCPToken,
		},
	}
	for _, item := range luckinTools.Tools(luckinClient) {
		bound := tool.BindSurface(item, policy.Surface, "luckin")
		if integrations.Luckin.MCPToken != "" {
			_ = catalog.RegisterVisible(policy, bound)
		} else {
			_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, bound)
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
	_ = catalog.RegisterVisible(policy, webSearchTools.SearchTool{Client: webClient})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, webSearchTools.ReadPageTool{Client: webClient})
	_ = catalog.RegisterVisible(policy, knowledgeTools.RunbookSearchTool{})
}

func registerAgentControlTools(catalog *tool.Catalog, policy tool.SurfacePolicy, userPrefs userprefs.Store) {
	_ = catalog.RegisterVisible(policy, plannerTools.PlanTool{})
	_ = catalog.RegisterVisible(policy, skillTools.LoadTool{UserPrefs: userPrefs})
}

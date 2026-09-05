package hostedtools

import (
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/mcp"
	"github.com/noknov/kepler-agent/packages/safety"
	clickstackTools "github.com/noknov/kepler-agent/packages/tools/clickstack"
	codeTools "github.com/noknov/kepler-agent/packages/tools/code"
	diagnosticsTools "github.com/noknov/kepler-agent/packages/tools/diagnostics"
	gcpTools "github.com/noknov/kepler-agent/packages/tools/gcp"
	gitTools "github.com/noknov/kepler-agent/packages/tools/git"
	githubTools "github.com/noknov/kepler-agent/packages/tools/github"
	k8sTools "github.com/noknov/kepler-agent/packages/tools/k8s"
	knowledgeTools "github.com/noknov/kepler-agent/packages/tools/knowledge"
	luckinTools "github.com/noknov/kepler-agent/packages/tools/luckin"
	notionTools "github.com/noknov/kepler-agent/packages/tools/notion"
	plannerTools "github.com/noknov/kepler-agent/packages/tools/planner"
	skillTools "github.com/noknov/kepler-agent/packages/tools/skills"
	webSearchTools "github.com/noknov/kepler-agent/packages/tools/websearch"
	workspaceTools "github.com/noknov/kepler-agent/packages/tools/workspace"
	youtrackTools "github.com/noknov/kepler-agent/packages/tools/youtrack"
	"github.com/noknov/kepler-agent/packages/userprefs"
)

type SurfaceOptions struct {
	Name          string
	AvailableDeps map[string]bool
	Connections   *connections.Service
}

type CatalogBundle struct {
	Catalog    *tool.Catalog
	ClickStack *clickstackTools.Registrar
	Notion     *notionTools.Registrar
}

func NewCatalog(cfg config.Config, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy, userPrefs userprefs.Store, surface SurfaceOptions) (CatalogBundle, error) {
	policy := policyForSurface(cfg, surface)
	catalog, err := tool.NewCatalog()
	if err != nil {
		return CatalogBundle{}, err
	}
	registration := tool.NewRegistration(catalog, policy)
	registerDeferredDiagnosticsTools(registration)
	registerWorkspaceTools(registration, workspacePolicy)
	registerCodeTools(registration, cfg, workspacePolicy, commandPolicy)
	registerIntegrationTools(registration, cfg, commandPolicy, surface.Connections)
	clickstackReg := clickstackTools.NewRegistrar(cfg.Integrations.ClickStack, surface.Connections)
	notionReg := notionTools.NewRegistrar(cfg.Integrations.Notion, surface.Connections)
	registerKnowledgeTools(registration, cfg)
	registerAgentControlTools(registration, userPrefs)
	if err := registration.Err(); err != nil {
		return CatalogBundle{}, err
	}
	if err := catalog.Register(tool.NewSearchTool(catalog)); err != nil {
		return CatalogBundle{}, err
	}
	return CatalogBundle{Catalog: catalog, ClickStack: clickstackReg, Notion: notionReg}, nil
}

func PolicyForSurface(cfg config.Config, surface SurfaceOptions) tool.SurfacePolicy {
	return policyForSurface(cfg, surface)
}

func policyForSurface(cfg config.Config, surface SurfaceOptions) tool.SurfacePolicy {
	integrations := cfg.Integrations
	availableDeps := map[string]bool{
		"github":     integrations.GitHub.Token != "",
		"luckin":     integrations.Luckin.MCPToken != "",
		"clickstack": integrations.ClickStack.Enabled(),
		"gcp":        cfg.Connections.GCPOAuthEnabled() || integrations.GCP.DefaultProject != "" || strings.TrimSpace(integrations.GCP.GCloudPath) != "",
		"notion":     integrations.Notion.Enabled(),
		"tts":        integrations.TTS.APIKey != "",
		"youtrack":   integrations.YouTrack.URL != "" && integrations.YouTrack.Token != "",
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
	if surface.Connections != nil && surface.Connections.Config.GCPEnabled() {
		availableDeps["gcp-connection"] = true
	}
	if surface.Connections != nil && surface.Connections.Config.NotionEnabled() {
		availableDeps["notion-connection"] = true
	}
	return tool.SurfacePolicy{
		Surface:       surface.Name,
		AvailableDeps: availableDeps,
	}
}

func registerDeferredDiagnosticsTools(registration *tool.Registration) {
	registration.Deferred(tool.CategoryDiagnostics, diagnosticsTools.IncidentBriefTool{})
	registration.Deferred(tool.CategoryDiagnostics, diagnosticsTools.TimelineTool{})
	registration.Deferred(tool.CategoryDiagnostics, diagnosticsTools.EvidenceBoardTool{})
}

func registerWorkspaceTools(registration *tool.Registration, workspacePolicy safety.WorkspacePolicy) {
	if len(workspacePolicy.Roots) == 0 {
		return
	}
	registration.Deferred(tool.CategoryWorkspace, workspaceTools.ListReposTool{Roots: workspacePolicy.Roots})
}

func registerCodeTools(registration *tool.Registration, cfg config.Config, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy) {
	registration.Visible(codeTools.SearchTool{Paths: workspacePolicy})
	registration.Visible(codeTools.ReadFileTool{Paths: workspacePolicy})

	gitBase := gitTools.Base{Paths: workspacePolicy, Guard: commandPolicy, Timeout: cfg.Tools.CommandTimeout}
	registration.Visible(gitTools.RepoSearchTool{Base: gitBase})
	registration.Visible(gitTools.RepoReadFileTool{Base: gitBase})
	registration.Visible(gitTools.FetchRefTool{Base: gitBase})
	registration.Visible(gitTools.SearchRefTool{Base: gitBase})
	registration.Visible(gitTools.ReadFileRefTool{Base: gitBase})
	registration.Visible(gitTools.StatusTool{Base: gitBase})
	registration.Visible(gitTools.LogTool{Base: gitBase})
	registration.Visible(gitTools.ShowTool{Base: gitBase})
}

func registerIntegrationTools(registration *tool.Registration, cfg config.Config, commandPolicy safety.CommandPolicy, conn *connections.Service) {
	integrations := cfg.Integrations
	k8sDefaults := k8sTools.Defaults{
		Project:   integrations.GCP.DefaultProject,
		Region:    integrations.GCP.DefaultRegion,
		Cluster:   integrations.GCP.DefaultCluster,
		Namespace: integrations.K8s.DefaultNamespace,
	}
	if k8sDefaults.Namespace == "" {
		k8sDefaults.Namespace = integrations.GCP.DefaultNamespace
	}
	gcpDefaults := gcpTools.Defaults{
		Project:   integrations.GCP.DefaultProject,
		Namespace: integrations.GCP.DefaultNamespace,
		Cluster:   integrations.GCP.DefaultCluster,
		Region:    integrations.GCP.DefaultRegion,
	}
	gcpTimeout := cfg.Tools.CommandTimeout

	var k8sSource k8sTools.TokenSource
	var gcpSource gcpTools.TokenSource
	if conn != nil && conn.Config.GCPEnabled() {
		k8sSource = k8sTools.ConnectedSource{Service: *conn, Defaults: k8sDefaults}
		gcpSource = gcpTools.ConnectedSource{Service: *conn, Defaults: gcpDefaults}
	} else {
		local := gcpTools.LocalTokenSource{
			GCloudPath: integrations.GCP.GCloudPath,
			Defaults:   gcpDefaults,
			Timeout:    gcpTimeout,
		}
		gcpSource = local
		k8sSource = k8sTools.LocalTokenSource{
			GCloudPath: integrations.GCP.GCloudPath,
			Defaults:   k8sDefaults,
			Timeout:    gcpTimeout,
		}
	}
	registration.Deferred(tool.CategoryInfrastructure, k8sTools.ContextsTool{Source: k8sSource, Defaults: k8sDefaults, Timeout: gcpTimeout})
	registration.Deferred(tool.CategoryInfrastructure, k8sTools.GetPodsTool{Source: k8sSource, Defaults: k8sDefaults, Timeout: gcpTimeout})
	registration.Deferred(tool.CategoryInfrastructure, k8sTools.LogsTool{Source: k8sSource, Defaults: k8sDefaults, Timeout: gcpTimeout})
	registration.Deferred(tool.CategoryInfrastructure, k8sTools.DescribeTool{Source: k8sSource, Defaults: k8sDefaults, Timeout: gcpTimeout})
	registration.Deferred(tool.CategoryInfrastructure, k8sTools.TopTool{Source: k8sSource, Defaults: k8sDefaults, Timeout: gcpTimeout})
	registration.Deferred(tool.CategoryInfrastructure, k8sTools.EventsTool{Source: k8sSource, Defaults: k8sDefaults, Timeout: gcpTimeout})
	registration.Deferred(tool.CategoryInfrastructure, k8sTools.RolloutTool{Source: k8sSource, Defaults: k8sDefaults, Timeout: gcpTimeout})
	registration.Deferred(tool.CategoryInfrastructure, k8sTools.GetTool{Source: k8sSource, Defaults: k8sDefaults, Timeout: gcpTimeout})
	registration.Deferred(tool.CategoryInfrastructure, gcpTools.LogsTool{
		Source:   gcpSource,
		Defaults: gcpDefaults,
		Timeout:  gcpTimeout,
	})
	registration.Deferred(tool.CategoryInfrastructure, gcpTools.RunServicesTool{
		Source:   gcpSource,
		Defaults: gcpDefaults,
		Timeout:  gcpTimeout,
	})
	registration.Deferred(tool.CategoryInfrastructure, gcpTools.RunRevisionsTool{
		Source:   gcpSource,
		Defaults: gcpDefaults,
		Timeout:  gcpTimeout,
	})
	registration.Deferred(tool.CategoryInfrastructure, gcpTools.ClustersTool{
		Source:   gcpSource,
		Defaults: gcpDefaults,
		Timeout:  gcpTimeout,
	})

	youtrackClient := youtrackTools.Client{BaseURL: integrations.YouTrack.URL, Token: integrations.YouTrack.Token}
	registration.Deferred(tool.CategoryIntegration, youtrackTools.GetIssueTool{Client: youtrackClient})
	registration.Deferred(tool.CategoryIntegration, youtrackTools.SearchTool{Client: youtrackClient})

	githubClient := githubTools.Client{
		Token:      integrations.GitHub.Token,
		APIBaseURL: integrations.GitHub.APIBaseURL,
		Owner:      integrations.GitHub.DefaultOwner,
		Repo:       integrations.GitHub.DefaultRepo,
	}
	registration.Deferred(tool.CategoryIntegration, githubTools.DispatchWorkflowTool{Client: githubClient})
	registration.Deferred(tool.CategoryIntegration, githubTools.WorkflowRunsTool{Client: githubClient})
	registration.Deferred(tool.CategoryIntegration, githubTools.PRDiffTool{Client: githubClient})
	registration.Deferred(tool.CategoryIntegration, githubTools.PRFileDiffTool{Client: githubClient})
	registration.Deferred(tool.CategoryIntegration, githubTools.JobLogsTool{Client: githubClient})

	luckinClient := &luckinTools.Client{
		MCP: &mcp.Client{
			ServiceName: "luckin",
			URL:         integrations.Luckin.MCPURL,
			Token:       integrations.Luckin.MCPToken,
		},
	}
	for _, item := range luckinTools.Tools(luckinClient) {
		bound := tool.BindSurface(item, registration.Surface(), "luckin")
		if integrations.Luckin.MCPToken != "" {
			registration.Visible(bound)
		} else {
			registration.Deferred(tool.CategoryIntegration, bound)
		}
	}
}

func registerKnowledgeTools(registration *tool.Registration, cfg config.Config) {
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
	registration.Visible(webSearchTools.SearchTool{Client: webClient})
	registration.Deferred(tool.CategoryIntegration, webSearchTools.ReadPageTool{Client: webClient})
	registration.Visible(knowledgeTools.RunbookSearchTool{})
}

func registerAgentControlTools(registration *tool.Registration, userPrefs userprefs.Store) {
	registration.Visible(plannerTools.PlanTool{})
	registration.Visible(skillTools.LoadTool{UserPrefs: userPrefs})
}

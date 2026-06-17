package app

import (
	"github.com/wati/oncall-agent/internal/codeintel"
	"github.com/wati/oncall-agent/internal/config"
	"github.com/wati/oncall-agent/internal/delegation"
	"github.com/wati/oncall-agent/internal/llm"
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

func newToolRegistry(cfg config.Config, slackClient *slack.Client, llmClient llm.Client, workspacePolicy safety.WorkspacePolicy, commandPolicy safety.CommandPolicy) *registry.Registry {
	tools := registry.New()
	registerDiagnosticsTools(tools)
	registerCodeTools(tools, cfg, workspacePolicy, commandPolicy)
	registerIntegrationTools(tools, cfg, commandPolicy)
	registerKnowledgeTools(tools)
	registerSlackTools(tools, slackClient)
	registerAgentControlTools(tools, cfg, llmClient)
	return tools
}

func registerDiagnosticsTools(tools *registry.Registry) {
	tools.Register(diagnosticsTools.IncidentBriefTool{})
	tools.Register(diagnosticsTools.TimelineTool{})
	tools.Register(diagnosticsTools.EvidenceBoardTool{})
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
}

func registerKnowledgeTools(tools *registry.Registry) {
	tools.Register(webSearchTools.ReadPageTool{Client: webSearchTools.Client{}})
	tools.Register(knowledgeTools.RunbookSearchTool{})
}

func registerSlackTools(tools *registry.Registry, slackClient *slack.Client) {
	tools.Register(slacktool.AskUserTool{Slack: slackClient})
	tools.Register(slacktool.FileSearchTool{Slack: slackClient})
	tools.Register(slacktool.JSONAnalyzeTool{Slack: slackClient})
}

func registerAgentControlTools(tools *registry.Registry, cfg config.Config, llmClient llm.Client) {
	delegates := delegation.NewManager(llmClient, cfg.LLM.Model, cfg.LLM.Thinking)
	delegates.SetPolicyPrompt(prompts.RulesAndSkillsPrompt())
	tools.Register(skillTools.LoadTool{})
	tools.Register(delegation.Tool{Manager: delegates})
}

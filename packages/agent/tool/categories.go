package tool

const (
	CategoryDiagnostics    = "diagnostics"
	CategoryCode           = "code"
	CategoryWorkspace      = "workspace"
	CategoryIntegration    = "integration"
	CategoryInfrastructure = "infrastructure"
)

var categoryDescriptions = map[string]string{
	CategoryDiagnostics:    "Incident investigation helpers: incident briefs, timelines, and evidence boards.",
	CategoryCode:           "Advanced code intelligence: static package graphs, symbols, definitions, references, callers, callees, callgraphs, and impact analysis.",
	CategoryWorkspace:      "Workspace discovery helpers: list git repositories under configured workspace roots.",
	CategoryIntegration:    "External integrations: GitHub, Notion, YouTrack, Slack Canvas, Luckin MCP, TTS, and related APIs.",
	CategoryInfrastructure: "Infrastructure and operations tools: Kubernetes, GCP Cloud Logging, Cloud Run, clusters, pods, logs, events, rollouts, and ClickStack observability MCP.",
}

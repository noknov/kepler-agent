package tool

const (
	CategoryWorkspace      = "workspace"
	CategoryIntegration    = "integration"
	CategoryInfrastructure = "infrastructure"
)

var categoryDescriptions = map[string]string{
	CategoryWorkspace:      "Workspace discovery helpers: list git repositories under configured workspace roots.",
	CategoryIntegration:    "External integrations: GitHub, Notion, YouTrack, Slack Canvas, Luckin MCP, TTS, and related APIs.",
	CategoryInfrastructure: "Infrastructure and operations tools: Kubernetes, GCP Cloud Logging, Cloud Run, clusters, pods, logs, events, rollouts, and ClickStack observability MCP.",
}

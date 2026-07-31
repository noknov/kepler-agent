package runtime

import (
	"os"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/config"
)

func LocalCLIConfig() config.Config {
	wd, _ := os.Getwd()
	return config.Config{
		LLM: config.LLMConfig{
			Provider: "local",
			Protocol: "openai",
			Model:    "local",
		},
		Security: config.SecurityConfig{
			WorkspaceRoots: []string{wd},
		},
		Sessions: config.SessionConfig{
			MaxContextTokens:    200000,
			AutocompactBuffer:   13000,
			MaxToolResultTokens: 8000,
		},
		Tools: config.ToolConfig{
			CommandTimeout:         30 * time.Second,
			AgentMaxSteps:          256,
			AgentMaxConcurrentRuns: 1,
		},
		Integrations: config.IntegrationConfig{
			GCP: config.GCPConfig{
				GCloudPath: "gcloud",
			},
			K8s: config.K8sConfig{
				KubectlPath: "kubectl",
			},
			GitHub: config.GitHubConfig{
				APIBaseURL: "https://api.github.com",
			},
			Luckin: config.LuckinConfig{
				MCPURL: "https://gwmcp.lkcoffee.com/order/user/mcp",
			},
			TTS: config.TTSConfig{
				BaseURL: "https://token-plan-cn.xiaomimimo.com/v1",
				Model:   "mimo-v2.5-tts",
			},
			WebSearch: config.WebSearchConfig{
				Provider:   "duckduckgo",
				SerpAPIURL: "https://serpapi.com/search.json",
				BraveURL:   "https://api.search.brave.com/res/v1/web/search",
			},
		},
	}
}

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
			GCloudPath:             "gcloud",
			KubectlPath:            "kubectl",
			GitHubAPIBaseURL:       "https://api.github.com",
			LuckinMCPURL:           "https://gwmcp.lkcoffee.com/order/user/mcp",
			TTSBaseURL:             "https://token-plan-cn.xiaomimimo.com/v1",
			TTSModel:               "mimo-v2.5-tts",
			WebSearchProvider:      "duckduckgo",
			WebSearchSerpAPIURL:    "https://serpapi.com/search.json",
			WebSearchBraveURL:      "https://api.search.brave.com/res/v1/web/search",
		},
	}
}

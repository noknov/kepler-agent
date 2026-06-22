package observability

import (
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
)

type CostRates struct {
	InputPerMTok         float64
	OutputPerMTok        float64
	CacheReadPerMTok     float64
	CacheCreationPerMTok float64
}

func (r CostRates) EstimateUSD(usage llm.Usage) float64 {
	inputTokens := usage.PromptTokens - usage.CacheReadInputTokens - usage.CacheCreationInputTokens
	if inputTokens < 0 {
		inputTokens = usage.PromptTokens
	}
	cost := float64(inputTokens) * r.InputPerMTok / 1_000_000
	cost += float64(usage.CompletionTokens) * r.OutputPerMTok / 1_000_000
	cost += float64(usage.CacheReadInputTokens) * r.CacheReadPerMTok / 1_000_000
	cost += float64(usage.CacheCreationInputTokens) * r.CacheCreationPerMTok / 1_000_000
	return cost
}

func DefaultCostRates(provider, model string) CostRates {
	key := strings.ToLower(strings.TrimSpace(provider + " " + model))
	switch {
	case strings.Contains(key, "gpt-4o-mini"):
		return CostRates{InputPerMTok: 0.15, OutputPerMTok: 0.60, CacheReadPerMTok: 0.075}
	case strings.Contains(key, "gpt-4o"):
		return CostRates{InputPerMTok: 2.50, OutputPerMTok: 10.00, CacheReadPerMTok: 1.25}
	case strings.Contains(key, "claude") && strings.Contains(key, "sonnet"):
		return CostRates{InputPerMTok: 3.00, OutputPerMTok: 15.00, CacheReadPerMTok: 0.30, CacheCreationPerMTok: 3.75}
	case strings.Contains(key, "opencode-go glm-5.2") || strings.Contains(key, "opencode-go glm-5.1"):
		return CostRates{InputPerMTok: 1.40, OutputPerMTok: 4.40, CacheReadPerMTok: 0.26}
	case strings.Contains(key, "opencode-go kimi-k2.7-code"):
		return CostRates{InputPerMTok: 0.95, OutputPerMTok: 4.00, CacheReadPerMTok: 0.19}
	case strings.Contains(key, "opencode-go kimi-k2.6"):
		return CostRates{InputPerMTok: 0.95, OutputPerMTok: 4.00, CacheReadPerMTok: 0.16}
	case strings.Contains(key, "opencode-go mimo-v2.5-pro"):
		return CostRates{InputPerMTok: 1.74, OutputPerMTok: 3.48, CacheReadPerMTok: 0.0145}
	case strings.Contains(key, "opencode-go mimo-v2.5"):
		return CostRates{InputPerMTok: 0.14, OutputPerMTok: 0.28, CacheReadPerMTok: 0.0028}
	case strings.Contains(key, "opencode-go minimax-m3"):
		return CostRates{InputPerMTok: 0.30, OutputPerMTok: 1.20, CacheReadPerMTok: 0.06}
	case strings.Contains(key, "opencode-go minimax-m2.7") || strings.Contains(key, "opencode-go minimax-m2.5"):
		return CostRates{InputPerMTok: 0.30, OutputPerMTok: 1.20, CacheReadPerMTok: 0.06, CacheCreationPerMTok: 0.375}
	case strings.Contains(key, "opencode-go qwen3.7-max"):
		return CostRates{InputPerMTok: 2.50, OutputPerMTok: 7.50, CacheReadPerMTok: 0.50, CacheCreationPerMTok: 3.125}
	case strings.Contains(key, "opencode-go qwen3.7-plus"):
		return CostRates{InputPerMTok: 0.40, OutputPerMTok: 1.60, CacheReadPerMTok: 0.04, CacheCreationPerMTok: 0.50}
	case strings.Contains(key, "opencode-go qwen3.6-plus"):
		return CostRates{InputPerMTok: 0.50, OutputPerMTok: 3.00, CacheReadPerMTok: 0.05, CacheCreationPerMTok: 0.625}
	case strings.Contains(key, "opencode-go deepseek-v4-pro"):
		return CostRates{InputPerMTok: 1.74, OutputPerMTok: 3.48, CacheReadPerMTok: 0.0145}
	case strings.Contains(key, "opencode-go deepseek-v4-flash"):
		return CostRates{InputPerMTok: 0.14, OutputPerMTok: 0.28, CacheReadPerMTok: 0.0028}
	case strings.Contains(key, "kimi") || strings.Contains(key, "moonshot") || strings.Contains(key, "mimo"):
		return CostRates{}
	default:
		return CostRates{}
	}
}

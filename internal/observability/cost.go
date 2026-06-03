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
	case strings.Contains(key, "kimi") || strings.Contains(key, "moonshot") || strings.Contains(key, "mimo"):
		return CostRates{}
	default:
		return CostRates{}
	}
}

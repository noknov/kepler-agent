package observability

import (
	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/llm"
)

type CostRates struct {
	InputPerMTok         float64
	OutputPerMTok        float64
	CacheReadPerMTok     float64
	CacheCreationPerMTok float64
}

func (r CostRates) EstimateUSD(usage llm.Usage) float64 {
	inputTokens := usage.PromptTokens
	if usage.CacheIncludedInPrompt {
		inputTokens -= usage.CacheReadInputTokens + usage.CacheCreationInputTokens
		if inputTokens < 0 {
			inputTokens = usage.PromptTokens
		}
	}
	cost := float64(inputTokens) * r.InputPerMTok / 1_000_000
	cost += float64(usage.CompletionTokens) * r.OutputPerMTok / 1_000_000
	cost += float64(usage.CacheReadInputTokens) * r.CacheReadPerMTok / 1_000_000
	cost += float64(usage.CacheCreationInputTokens) * r.CacheCreationPerMTok / 1_000_000
	return cost
}

func UsageFromModel(value model.Usage) llm.Usage {
	return llm.Usage{
		PromptTokens:             int(value.InputTokens),
		CompletionTokens:         int(value.OutputTokens),
		TotalTokens:              int(value.InputTokens + value.OutputTokens),
		CacheReadInputTokens:     int(value.CacheReadTokens),
		CacheCreationInputTokens: int(value.CacheCreatedTokens),
		CacheIncludedInPrompt:    value.CacheTokensIncludedInInput,
	}
}

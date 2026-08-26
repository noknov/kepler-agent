package observability

import (
	"testing"

	"github.com/noknov/kepler-agent/packages/llm"
)

func TestCostRatesEstimateUSD(t *testing.T) {
	rates := CostRates{InputPerMTok: 1, OutputPerMTok: 2}
	got := rates.EstimateUSD(llm.Usage{PromptTokens: 1_000_000, CompletionTokens: 500_000})
	if got != 2 {
		t.Fatalf("EstimateUSD() = %v, want 2", got)
	}
}

func TestCostRatesRespectProviderCacheAccounting(t *testing.T) {
	rates := CostRates{InputPerMTok: 1, CacheReadPerMTok: 0.1}
	openAI := rates.EstimateUSD(llm.Usage{PromptTokens: 1_000_000, CacheReadInputTokens: 500_000, CacheIncludedInPrompt: true})
	anthropic := rates.EstimateUSD(llm.Usage{PromptTokens: 1_000_000, CacheReadInputTokens: 500_000, CacheIncludedInPrompt: false})
	if openAI != 0.55 || anthropic != 1.05 {
		t.Fatalf("cache costs: openai=%v anthropic=%v", openAI, anthropic)
	}
}

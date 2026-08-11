package observability

import (
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/llm"
)

func TestCostRatesEstimateUSD(t *testing.T) {
	rates := CostRates{InputPerMTok: 1, OutputPerMTok: 2}
	got := rates.EstimateUSD(llm.Usage{PromptTokens: 1_000_000, CompletionTokens: 500_000})
	if got != 2 {
		t.Fatalf("EstimateUSD() = %v, want 2", got)
	}
}

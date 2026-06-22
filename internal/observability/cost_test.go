package observability

import "testing"

func TestDefaultCostRatesOpenCodeGo(t *testing.T) {
	rates := DefaultCostRates("opencode-go", "kimi-k2.7-code")
	if rates.InputPerMTok != 0.95 || rates.OutputPerMTok != 4.00 || rates.CacheReadPerMTok != 0.19 {
		t.Fatalf("rates = %#v, want Kimi K2.7 Code OpenCode Go rates", rates)
	}
}

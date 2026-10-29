package model

import "testing"

func TestUsageAdditionNormalizesMixedCacheConventions(t *testing.T) {
	a := Usage{Reported: true, InputTokens: 100, OutputTokens: 10, CacheReadTokens: 40, CacheTokensIncludedInInput: true, ReasoningTokens: 3}
	b := Usage{InputTokens: 60, OutputTokens: 10, CacheReadTokens: 20, CacheCreatedTokens: 5, ReasoningTokens: 4}
	c := Usage{InputTokens: 8, CacheCreatedTokens: 2}
	want := Usage{Reported: true, InputTokens: 185, OutputTokens: 20, CacheReadTokens: 60, CacheCreatedTokens: 5, CacheTokensIncludedInInput: true, ReasoningTokens: 7}
	if a.Add(b) != want || b.Add(a) != want {
		t.Fatalf("mixed usage=%+v / %+v", a.Add(b), b.Add(a))
	}
	if a.Add(b).Add(c) != a.Add(b.Add(c)) || UsageTokens(a.Add(b).Add(c)) != UsageTokens(a)+UsageTokens(b)+UsageTokens(c) {
		t.Fatal("grouping changed usage or token allowance")
	}
	if (Usage{}).Add(Usage{}).Known() {
		t.Fatal("aggregation invented reported usage")
	}
	if !(Usage{}).Add(Usage{Reported: true}).Known() {
		t.Fatal("explicit zero usage was lost")
	}
}

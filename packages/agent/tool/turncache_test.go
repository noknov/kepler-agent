package tool

import "testing"

func TestTurnCacheKeepsOwnersAndCleanupSeparate(t *testing.T) {
	a := Scope{SessionID: t.Name() + "a\x00b", TurnID: "c"}
	b := Scope{SessionID: t.Name() + "a", TurnID: "b\x00c"}
	t.Cleanup(func() { ClearTurnCache(a.SessionID, a.TurnID); ClearTurnCache(b.SessionID, b.TurnID) })
	CacheFor(a).Set("marker", "first owner")
	if value, ok := CacheFor(b).Get("marker"); ok {
		t.Errorf("another owner read scratch state: %v", value)
	}
	CacheFor(b).Set("marker", "second owner")
	ClearTurnCache(a.SessionID, a.TurnID)
	if value, ok := CacheFor(b).Get("marker"); !ok || value != "second owner" {
		t.Errorf("cleanup removed another owner's state: %v/%v", value, ok)
	}
	if _, ok := CacheFor(a).Get("marker"); ok {
		t.Error("cleared owner retained scratch state")
	}
}

func BenchmarkWarmTurnCache(b *testing.B) {
	scope := Scope{SessionID: "benchmark-session", TurnID: "benchmark-turn"}
	CacheFor(scope).Set("key", "value")
	defer ClearTurnCache(scope.SessionID, scope.TurnID)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		CacheFor(scope).Get("key")
	}
}

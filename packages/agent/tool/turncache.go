package tool

import (
	"sync"
)

// TurnCache provides per-turn scratch state shared by tools in the same step
// batch. Values do not survive turn boundaries.
type TurnCache struct {
	mu     sync.Mutex
	values map[string]any
}

func (c *TurnCache) Get(key string) (any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[key]
	return value, ok
}

func (c *TurnCache) Set(key string, value any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = map[string]any{}
	}
	c.values[key] = value
}

var turnCaches sync.Map

func turnCacheKey(scope Scope) string {
	return scope.SessionID + "\x00" + scope.TurnID
}

// CacheFor returns the turn-scoped cache for a tool call.
func CacheFor(scope Scope) *TurnCache {
	key := turnCacheKey(scope)
	if existing, ok := turnCaches.Load(key); ok {
		return existing.(*TurnCache)
	}
	cache := &TurnCache{values: map[string]any{}}
	actual, _ := turnCaches.LoadOrStore(key, cache)
	return actual.(*TurnCache)
}

// ClearTurnCache drops scratch state for a finished turn.
func ClearTurnCache(sessionID, turnID string) {
	turnCaches.Delete(sessionID + "\x00" + turnID)
}

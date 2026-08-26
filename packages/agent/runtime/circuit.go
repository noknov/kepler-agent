package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

// CircuitBreakerConfig optionally blocks repeated identical tool calls.
type CircuitBreakerConfig struct {
	Enabled                    bool
	MaxIdenticalFailedAttempts int
}

func (c CircuitBreakerConfig) withDefaults() CircuitBreakerConfig {
	if c.MaxIdenticalFailedAttempts <= 0 {
		c.MaxIdenticalFailedAttempts = 3
	}
	return c
}

type callFingerprint struct {
	name string
	args string
}

type circuitState struct {
	mu       sync.Mutex
	failures map[callFingerprint]int
}

func newCircuitState() *circuitState {
	return &circuitState{
		failures: make(map[callFingerprint]int),
	}
}

func (s *circuitState) check(config CircuitBreakerConfig, call tool.Call) error {
	config = config.withDefaults()
	if !config.Enabled {
		return nil
	}
	fp := fingerprint(call)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures[fp] >= config.MaxIdenticalFailedAttempts {
		return fmt.Errorf("circuit breaker blocked repeated failed tool call %q", call.Name)
	}
	return nil
}

func (s *circuitState) record(config CircuitBreakerConfig, call tool.Call, failed bool) {
	config = config.withDefaults()
	if !config.Enabled {
		return
	}
	fp := fingerprint(call)
	s.mu.Lock()
	defer s.mu.Unlock()
	if failed {
		s.failures[fp]++
		return
	}
	// A successful call is evidence that the operation is healthy. Blocking it
	// can turn legitimate polling or repeated reads into a false failure.
}

func fingerprint(call tool.Call) callFingerprint {
	sum := sha256.Sum256(call.Arguments)
	return callFingerprint{name: call.Name, args: hex.EncodeToString(sum[:8])}
}

var circuitBreakers sync.Map

func circuitFor(sessionID string) *circuitState {
	if existing, ok := circuitBreakers.Load(sessionID); ok {
		return existing.(*circuitState)
	}
	state := newCircuitState()
	actual, _ := circuitBreakers.LoadOrStore(sessionID, state)
	return actual.(*circuitState)
}

func clearCircuit(sessionID string) {
	circuitBreakers.Delete(sessionID)
}

func (r *Runtime) checkCircuit(ctx context.Context, call tool.Call) error {
	config := r.config.CircuitBreaker.withDefaults()
	if !config.Enabled {
		return nil
	}
	return circuitFor(call.Scope.SessionID).check(config, call)
}

func (r *Runtime) recordCircuit(call tool.Call, failed bool) {
	config := r.config.CircuitBreaker.withDefaults()
	circuitFor(call.Scope.SessionID).record(config, call, failed)
}

func (r *Runtime) clearCircuit(sessionID string) {
	clearCircuit(sessionID)
}

// CircuitWarning returns a warning message when a call is approaching limits.
func CircuitWarning(config CircuitBreakerConfig, call tool.Call, failed bool) string {
	config = config.withDefaults()
	state := circuitFor(call.Scope.SessionID)
	fp := fingerprint(call)
	state.mu.Lock()
	defer state.mu.Unlock()
	if failed {
		count := state.failures[fp]
		if count+1 >= config.MaxIdenticalFailedAttempts {
			return fmt.Sprintf("Warning: tool %q has failed %d times with identical arguments.", call.Name, count)
		}
		return ""
	}
	return ""
}

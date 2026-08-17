package runtime

import (
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

func TestCircuitBreakerBlocksRepeatedFailures(t *testing.T) {
	config := CircuitBreakerConfig{Enabled: true, MaxIdenticalFailedAttempts: 2}
	state := newCircuitState()
	call := tool.Call{Name: "demo", Arguments: []byte(`{"x":1}`), Scope: tool.Scope{SessionID: "s1", TurnID: "t1"}}
	if err := state.check(config, call); err != nil {
		t.Fatal(err)
	}
	state.record(config, call, true)
	state.record(config, call, true)
	if err := state.check(config, call); err == nil {
		t.Fatal("expected circuit breaker to block repeated failures")
	}
}

func TestCircuitBreakerDisabled(t *testing.T) {
	config := CircuitBreakerConfig{Enabled: false}
	state := newCircuitState()
	call := tool.Call{Name: "demo", Arguments: []byte(`{"x":1}`)}
	for range 10 {
		state.record(config, call, true)
		if err := state.check(config, call); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntimeCheckCircuit(t *testing.T) {
	config := CircuitBreakerConfig{Enabled: true, MaxIdenticalFailedAttempts: 1}
	state := newCircuitState()
	call := tool.Call{Name: "echo", Arguments: []byte(`{"x":1}`), Scope: tool.Scope{SessionID: "s1", TurnID: "t1"}}
	if err := state.check(config, call); err != nil {
		t.Fatal(err)
	}
	state.record(config, call, true)
	if err := state.check(config, call); err == nil {
		t.Fatal("expected blocked call")
	}
}

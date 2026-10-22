package memorytools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/memory"
)

type recordingStore struct {
	owner      memory.Identity
	generation int64
	record     memory.Record
	calls      int
}

func (s *recordingStore) Snapshot(_ context.Context, id memory.Identity) (int64, []memory.Record, error) {
	s.owner = id
	s.calls++
	return 7, nil, nil
}
func (s *recordingStore) Put(_ context.Context, id memory.Identity, generation int64, record memory.Record) error {
	s.owner = id
	s.generation = generation
	s.record = record
	s.calls++
	return nil
}
func (s *recordingStore) Forget(_ context.Context, id memory.Identity, generation int64, _ string) error {
	s.owner = id
	s.generation = generation
	s.calls++
	return nil
}
func (s *recordingStore) Reset(context.Context, memory.Identity) error { return nil }

func TestHostedMemoryUsesAuthenticatedScopeAndOriginalGeneration(t *testing.T) {
	store := &recordingStore{}
	item := Tool{Store: store}
	call := tool.Call{Scope: tool.Scope{UserID: "U1", SessionID: "s", TurnID: "t", Values: map[string]string{"team_id": "T1", "memory_generation": "3"}}, Arguments: json.RawMessage(`{"action":"upsert","semantic_key":"style","content":"use Go","expected_version":5,"user_id":"victim","team_id":"other","generation":7}`)}
	d, err := tool.DescribeCall(item, call)
	if err != nil || d.Parallel || d.Effects[0] != tool.EffectExternalWrite {
		t.Fatalf("descriptor=%+v err=%v", d, err)
	}
	if _, err := item.Execute(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if store.owner != (memory.Identity{TeamID: "T1", UserID: "U1"}) || store.generation != 3 || store.record.Version != 5 || store.record.TurnID != "t" {
		t.Fatalf("untrusted identity/version used: %+v", store)
	}
	call.Scope.Values = map[string]string{}
	if _, err := item.Execute(context.Background(), call); err == nil || store.calls != 1 {
		t.Fatal("missing identity reached the memory store")
	}
}

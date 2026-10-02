package delegation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// Capacity-only stand-in for the shared advisory-lock connection pool. It
// exercises the real parent runtime and delegation path, not PostgreSQL.
type boundedLease struct {
	slots        chan struct{}
	childWaiting atomic.Int32
}

func (l *boundedLease) Lock(ctx context.Context, id string) (func(), error) {
	if strings.Contains(id, "explore") {
		l.childWaiting.Add(1)
	}
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type delegatingParentModel struct {
	parents  atomic.Int32
	children atomic.Int32
	ready    chan struct{}
}

func (m *delegatingParentModel) Generate(ctx context.Context, r model.Request, _ model.EventSink) (model.Response, error) {
	if strings.HasPrefix(r.Metadata["session_id"], "explore") {
		m.children.Add(1)
		return model.Response{Message: model.TextMessage(model.RoleAssistant, "child report")}, nil
	}
	if m.parents.Add(1) == 8 {
		close(m.ready)
	}
	select {
	case <-m.ready:
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	}
	call := model.ToolCall{ID: "explore_call", Name: "agent-explore", Arguments: json.RawMessage(`{"task":"inspect code"}`)}
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}}}, nil
}
func TestChildrenCompleteWithSaturatedParentLeasePool(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lease := &boundedLease{slots: make(chan struct{}, 8)}
	m := &delegatingParentModel{ready: make(chan struct{})}
	store := transcript.NewMemoryStore()
	cat, _ := tool.NewCatalog(echoReadTool{})
	deps := agentruntime.Dependencies{Model: m, Tools: cat, Transcript: store, Lease: lease}
	runner := Runner{Config: agentruntime.Config{MaxSteps: 1}, Deps: deps, ParentCatalog: cat, AllowedTools: map[string]bool{"search": true}, MaxSteps: 1}
	if err := cat.Register(ExploreTool{Runner: runner}); err != nil {
		t.Fatal(err)
	}
	runtime, err := agentruntime.New(agentruntime.Config{MaxSteps: 1}, deps)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = runtime.RunTurn(ctx, agentruntime.TurnRequest{SessionID: fmt.Sprintf("parent_%d", i), TurnID: fmt.Sprintf("turn_%d", i), Input: model.TextMessage(model.RoleUser, "explore")})
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("parents and children failed to complete")
	}
	if got := m.children.Load(); got != 8 {
		t.Fatalf("child model calls = %d, want 8", got)
	}
	if got := lease.childWaiting.Load(); got != 0 {
		t.Fatalf("private children acquired %d shared leases", got)
	}
}

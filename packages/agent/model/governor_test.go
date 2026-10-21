package model

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingClient struct {
	active, peak atomic.Int32
	release      chan struct{}
}

func (c *blockingClient) Generate(ctx context.Context, _ Request, _ EventSink) (Response, error) {
	active := c.active.Add(1)
	defer c.active.Add(-1)
	for old := c.peak.Load(); active > old; old = c.peak.Load() {
		if c.peak.CompareAndSwap(old, active) {
			break
		}
	}
	select {
	case <-c.release:
		return Response{Usage: Usage{Reported: true, InputTokens: 2}}, nil
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
}

func TestGovernorSharesSlotsAndCancellationAcrossClients(t *testing.T) {
	g := NewGovernor(2)
	raw := &blockingClient{release: make(chan struct{})}
	first, second := Govern(raw, g), Govern(raw, g)
	var wait sync.WaitGroup
	for _, client := range []Client{first, second} {
		wait.Add(1)
		go func(c Client) { defer wait.Done(); _, _ = c.Generate(context.Background(), Request{}, nil) }(client)
	}
	deadline := time.Now().Add(time.Second)
	for raw.active.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := first.Generate(ctx, Request{}, nil); err == nil {
		t.Fatal("queued request ignored cancellation")
	}
	close(raw.release)
	wait.Wait()
	if raw.peak.Load() != 2 {
		t.Fatalf("peak=%d", raw.peak.Load())
	}
}

func TestParentTokenBudgetReservesAcrossWorkers(t *testing.T) {
	ctx := WithBudget(context.Background(), 100)
	budget := ctx.Value(budgetKey{}).(*Budget)
	if err := budget.reserve(ctx, 70); err != nil {
		t.Fatal(err)
	}
	if err := budget.reserve(ctx, 40); ErrorKindOf(err) != ErrorBudgetExhausted {
		t.Fatalf("overspend err=%v", err)
	}
	budget.settle(ctx, 70, Usage{Reported: true, InputTokens: 10, OutputTokens: 5}, nil)
	if err := budget.reserve(ctx, 80); err != nil {
		t.Fatal(err)
	}
	if WithBudget(ctx, 1000) != ctx {
		t.Fatal("child replaced parent's budget")
	}
}

func TestDurableBudgetRestoresUnknownReservations(t *testing.T) {
	var saved BudgetState
	record := func(_ context.Context, state BudgetState) error { saved = state; return nil }
	ctx := WithBudgetState(context.Background(), 100, BudgetState{}, record)
	budget := ctx.Value(budgetKey{}).(*Budget)
	if err := budget.reserve(ctx, 70); err != nil {
		t.Fatal(err)
	}
	// Simulate process loss before settlement: another context restores the ledger.
	resumed := WithBudgetState(context.Background(), 100, saved, record)
	restored := resumed.Value(budgetKey{}).(*Budget)
	if err := restored.reserve(resumed, 31); ErrorKindOf(err) != ErrorBudgetExhausted {
		t.Fatalf("unknown usage was refunded: %v", err)
	}
	if err := restored.reserve(resumed, 30); err != nil {
		t.Fatal(err)
	}
	if saved.Spent != 70 || saved.Reserved != 30 {
		t.Fatalf("state=%+v", saved)
	}
}

func TestDurableBudgetPersistenceFailureDoesNotDispatch(t *testing.T) {
	raw := &blockingClient{release: make(chan struct{})}
	close(raw.release)
	ctx := WithBudgetState(context.Background(), 100, BudgetState{}, func(context.Context, BudgetState) error { return context.DeadlineExceeded })
	_, err := Govern(raw, NewGovernor(1)).Generate(ctx, Request{InputTokenEstimate: 10, MaxOutputTokens: 10}, nil)
	if err == nil || raw.peak.Load() != 0 {
		t.Fatalf("provider dispatched without durable reservation: peak=%d err=%v", raw.peak.Load(), err)
	}
}

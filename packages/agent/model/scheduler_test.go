package model

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type clientFunc func(context.Context, Request, EventSink) (Response, error)

func (f clientFunc) Generate(ctx context.Context, r Request, s EventSink) (Response, error) {
	return f(ctx, r, s)
}

func waitQueued(t *testing.T, g *Governor, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if g.Snapshot().Queued == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queued=%d want %d", g.Snapshot().Queued, want)
}
func TestGovernorRotatesOwnersAndClassesAndCleansCanceledWaiters(t *testing.T) {
	g := NewGovernorWithConfig(GovernorConfig{Concurrency: 1, MaxQueued: 4})
	ctx := context.Background()
	held, _, err := g.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type admitted struct {
		owner   string
		class   WorkClass
		release func()
	}
	got := make(chan admitted, 4)
	enqueue := func(owner string, class WorkClass) {
		go func() {
			release, _, err := g.acquire(WithWorkload(ctx, Workload{owner, class}))
			if err != nil {
				t.Error(err)
				return
			}
			got <- admitted{owner, class, release}
		}()
	}
	enqueue("a", WorkBackground)
	waitQueued(t, g, 1)
	enqueue("a", WorkBackground)
	waitQueued(t, g, 2)
	enqueue("a", WorkInteractive)
	waitQueued(t, g, 3)
	enqueue("b", WorkInteractive)
	waitQueued(t, g, 4)
	if _, _, err := g.acquire(ctx); ErrorKindOf(err) != ErrorBudgetExhausted {
		t.Fatalf("unbounded admission: %v", err)
	}
	held()
	expected := []admitted{{owner: "a", class: WorkBackground}, {owner: "b", class: WorkInteractive}, {owner: "a", class: WorkInteractive}, {owner: "a", class: WorkBackground}}
	for _, want := range expected {
		select {
		case next := <-got:
			if next.owner != want.owner || next.class != want.class {
				t.Fatalf("got %s/%s want %s/%s", next.owner, next.class, want.owner, want.class)
			}
			next.release()
		case <-time.After(time.Second):
			t.Fatal("queued work starved")
		}
	}
	snapshot := g.Snapshot()
	if snapshot.Active != 0 || snapshot.Queued != 0 || snapshot.Owners != 0 {
		t.Fatalf("leaked admission state: %+v", snapshot)
	}
	held, _, _ = g.acquire(ctx)
	cancelCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, _, err := g.acquire(WithWorkload(cancelCtx, Workload{Owner: "c"})); done <- err }()
	waitQueued(t, g, 1)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	held()
	if snapshot := g.Snapshot(); snapshot.Queued != 0 || snapshot.Owners != 0 {
		t.Fatalf("cancel leaked owner %+v", snapshot)
	}
}

func TestRetryBackoffReleasesSlotAndChargesEachAttempt(t *testing.T) {
	g := NewGovernor(1)
	backoff := make(chan struct{})
	resume := make(chan struct{})
	attempts := 0
	raw := clientFunc(func(context.Context, Request, EventSink) (Response, error) {
		attempts++
		if attempts == 1 {
			return Response{Usage: Usage{Reported: true, InputTokens: 7}}, &Error{Kind: ErrorTransient, Retryable: true, Message: "temporary"}
		}
		return Response{Usage: Usage{Reported: true, InputTokens: 3}}, nil
	})
	resilient := &ResilientClient{Primary: raw, PrimaryProvider: "provider", MaxAttempts: 2, Sleep: func(context.Context, time.Duration) error { close(backoff); <-resume; return nil }}
	var mu sync.Mutex
	var records []AttemptUsage
	var state BudgetState
	ctx := WithBudgetState(context.Background(), 100, BudgetState{}, func(_ context.Context, next BudgetState) error { state = next; return nil })
	ctx = WithUsageRecorder(ctx, func(_ context.Context, record AttemptUsage) error {
		mu.Lock()
		defer mu.Unlock()
		records = append(records, record)
		return nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := Govern(resilient, g).Generate(ctx, Request{Model: "m", InputTokenEstimate: 10, MaxOutputTokens: 5}, nil)
		done <- err
	}()
	<-backoff
	other := clientFunc(func(context.Context, Request, EventSink) (Response, error) { return Response{}, nil })
	otherCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := Govern(other, g).Generate(otherCtx, Request{}, nil); err != nil {
		t.Fatalf("retry backoff held execution slot: %v", err)
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if state.Spent != 10 || state.Reserved != 0 {
		t.Fatalf("attempt costs not settled: %+v", state)
	}
	if len(records) != 2 || records[0].Charged != 7 || records[1].Charged != 3 || records[0].Provider != "provider" || records[1].Number != 2 {
		t.Fatalf("ledger=%+v", records)
	}
}

func TestUnknownFailedAttemptConsumesBudgetBeforeRetry(t *testing.T) {
	calls := 0
	raw := clientFunc(func(context.Context, Request, EventSink) (Response, error) {
		calls++
		return Response{}, &Error{Kind: ErrorTransient, Retryable: true, Message: "connection lost"}
	})
	c := &ResilientClient{Primary: raw, MaxAttempts: 3, Sleep: func(context.Context, time.Duration) error { return nil }}
	ctx := WithBudget(context.Background(), 25)
	_, err := Govern(c, NewGovernor(1)).Generate(ctx, Request{InputTokenEstimate: 10, MaxOutputTokens: 5}, nil)
	if ErrorKindOf(err) != ErrorBudgetExhausted || calls != 1 {
		t.Fatalf("retry overspent unknown usage: calls=%d err=%v", calls, err)
	}
}
func TestChildCannotReplaceSchedulingOwner(t *testing.T) {
	parent := WithWorkload(context.Background(), Workload{Owner: "tenant/user", Class: WorkInteractive})
	child := WithWorkload(parent, Workload{Owner: "other", Class: WorkBackground})
	if work := workloadFrom(child); work.Owner != "tenant/user" || work.Class != WorkBackground {
		t.Fatalf("work=%+v", work)
	}
}

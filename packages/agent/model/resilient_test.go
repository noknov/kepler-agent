package model

import (
	"context"
	"sync"
	"testing"
	"time"
)

type resilientScript struct {
	mu    sync.Mutex
	errs  []error
	calls int
}

func (s *resilientScript) Generate(_ context.Context, _ Request, _ EventSink) (Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(s.errs) == 0 {
		return Response{Message: TextMessage(RoleAssistant, "ok")}, nil
	}
	err := s.errs[0]
	s.errs = s.errs[1:]
	return Response{}, err
}

func transient() error { return &Error{Kind: ErrorTransient, Retryable: true, Message: "temporary"} }

func TestResilientClientRetriesTransientFailure(t *testing.T) {
	primary := &resilientScript{errs: []error{transient(), nil}}
	client := &ResilientClient{Primary: primary, MaxAttempts: 2, RetryDelay: time.Nanosecond}
	if _, err := client.Generate(context.Background(), Request{Model: "primary"}, nil); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 2 {
		t.Fatalf("calls = %d, want 2", primary.calls)
	}
}

func TestResilientClientFallsBackOnlyForRetryableFailure(t *testing.T) {
	primary := &resilientScript{errs: []error{transient(), transient()}}
	fallback := &resilientScript{}
	client := &ResilientClient{Primary: primary, Fallback: fallback, FallbackModel: "secondary", MaxAttempts: 2, RetryDelay: time.Nanosecond}
	if _, err := client.Generate(context.Background(), Request{Model: "primary"}, nil); err != nil {
		t.Fatal(err)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallback.calls)
	}

	primary = &resilientScript{errs: []error{&Error{Kind: ErrorAuth, Message: "bad key"}}}
	fallback = &resilientScript{}
	client = &ResilientClient{Primary: primary, Fallback: fallback, FallbackModel: "secondary"}
	_, err := client.Generate(context.Background(), Request{Model: "primary"}, nil)
	if ErrorKindOf(err) != ErrorAuth || fallback.calls != 0 {
		t.Fatalf("kind=%s fallback calls=%d", ErrorKindOf(err), fallback.calls)
	}
}

func TestResilientClientOpensCircuitPerProviderModel(t *testing.T) {
	now := time.Now()
	primary := &resilientScript{errs: []error{transient(), transient(), transient()}}
	client := &ResilientClient{Primary: primary, PrimaryProvider: "p", MaxAttempts: 1, FailureThreshold: 2, Cooldown: time.Minute, Now: func() time.Time { return now }}
	for range 2 {
		_, _ = client.Generate(context.Background(), Request{Model: "m"}, nil)
	}
	_, err := client.Generate(context.Background(), Request{Model: "m"}, nil)
	if ErrorKindOf(err) != ErrorCircuitOpen || primary.calls != 2 {
		t.Fatalf("kind=%s calls=%d", ErrorKindOf(err), primary.calls)
	}
	_, _ = client.Generate(context.Background(), Request{Model: "other"}, nil)
	if primary.calls != 3 {
		t.Fatalf("different model should not share circuit; calls=%d", primary.calls)
	}
}

func TestResilientClientDoesNotStartAttemptWithoutBudget(t *testing.T) {
	now := time.Now()
	primary := &resilientScript{}
	client := &ResilientClient{Primary: primary, MinAttemptBudget: 30 * time.Second, Now: func() time.Time { return now }}
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(5*time.Second))
	defer cancel()
	_, err := client.Generate(ctx, Request{Model: "m"}, nil)
	if ErrorKindOf(err) != ErrorBudgetExhausted || primary.calls != 0 {
		t.Fatalf("kind=%s calls=%d", ErrorKindOf(err), primary.calls)
	}
}

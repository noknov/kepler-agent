package model

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type resilientScript struct {
	mu    sync.Mutex
	errs  []error
	calls int
}

type streamingResilientScript struct {
	resilientScript
	events []StreamEvent
}

func (s *streamingResilientScript) Generate(ctx context.Context, request Request, sink EventSink) (Response, error) {
	s.mu.Lock()
	events := append([]StreamEvent(nil), s.events...)
	s.mu.Unlock()
	for _, event := range events {
		if sink != nil {
			if err := sink(event); err != nil {
				return Response{}, err
			}
		}
	}
	return s.resilientScript.Generate(ctx, request, sink)
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

func TestResilientClientExponentialBackoff(t *testing.T) {
	primary := &resilientScript{errs: []error{transient(), transient(), transient(), transient(), transient(), nil}}
	var delays []time.Duration
	client := &ResilientClient{
		Primary: primary, MaxAttempts: 6, RetryDelay: 5 * time.Second,
		Sleep: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	}
	if _, err := client.Generate(t.Context(), Request{Model: "test"}, nil); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 6 || len(delays) != 5 {
		t.Fatalf("calls=%d delays=%v", primary.calls, delays)
	}
	for i, ceiling := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second} {
		if delays[i] < ceiling/2 || delays[i] > ceiling {
			t.Fatalf("retry %d delay=%s outside [%s,%s]", i+1, delays[i], ceiling/2, ceiling)
		}
	}
	for _, base := range []time.Duration{0, time.Nanosecond, time.Duration(1<<63 - 1)} {
		client.RetryDelay = base
		if delay := client.retryDelay(1 << 30); delay < 15*time.Second || delay > 30*time.Second {
			t.Fatalf("base=%s overflowed or exceeded capped backoff: %s", base, delay)
		}
	}
}

func TestResilientClientBackoffRespectsBudgetAndCancellation(t *testing.T) {
	for _, cancelDuringWait := range []bool{false, true} {
		now := time.Now()
		ctx, cancel := context.WithDeadline(t.Context(), now.Add(46*time.Second))
		primary := &resilientScript{errs: []error{transient(), nil}}
		waited := false
		client := &ResilientClient{
			Primary: primary, MaxAttempts: 2, RetryDelay: 4 * time.Second,
			Now: func() time.Time { return now },
			Sleep: func(ctx context.Context, _ time.Duration) error {
				waited = true
				cancel()
				return ctx.Err()
			},
		}
		if cancelDuringWait {
			client.MinAttemptBudget = time.Second
		}
		_, err := client.Generate(ctx, Request{Model: "test"}, nil)
		cancel()
		if primary.calls != 1 || waited != cancelDuringWait {
			t.Fatalf("calls=%d waited=%v cancellation=%v", primary.calls, waited, cancelDuringWait)
		}
		if cancelDuringWait && !errors.Is(err, context.Canceled) || !cancelDuringWait && ErrorKindOf(err) != ErrorBudgetExhausted {
			t.Fatalf("wrong termination for cancellation=%v: %v", cancelDuringWait, err)
		}
	}
}

func TestResilientClientRetriesProviderProtocolFailure(t *testing.T) {
	protocol := &Error{Kind: ErrorProtocol, Retryable: true, Message: "malformed tool arguments"}
	primary := &resilientScript{errs: []error{protocol, nil}}
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

func TestResilientClientDefaultAllowsOneFastInitialAttempt(t *testing.T) {
	now := time.Now()
	primary := &resilientScript{}
	client := &ResilientClient{Primary: primary, Now: func() time.Time { return now }}
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(20*time.Second))
	defer cancel()
	if _, err := client.Generate(ctx, Request{Model: "m"}, nil); err != nil || primary.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, primary.calls)
	}
}

func TestResilientClientDoesNotRetryAfterCommittedText(t *testing.T) {
	primary := &streamingResilientScript{
		resilientScript: resilientScript{errs: []error{transient()}},
		events:          []StreamEvent{{Type: StreamTextDelta, Text: "partial"}},
	}
	fallback := &resilientScript{}
	client := &ResilientClient{Primary: primary, Fallback: fallback, FallbackModel: "secondary", MaxAttempts: 3, RetryDelay: time.Nanosecond}

	var received []StreamEvent
	_, err := client.Generate(context.Background(), Request{Model: "primary"}, func(event StreamEvent) error {
		received = append(received, event)
		return nil
	})
	if ErrorKindOf(err) != ErrorOutputCommitted {
		t.Fatalf("kind = %s, want %s", ErrorKindOf(err), ErrorOutputCommitted)
	}
	if primary.calls != 1 || fallback.calls != 0 {
		t.Fatalf("primary calls = %d, fallback calls = %d; want 1 and 0", primary.calls, fallback.calls)
	}
	if len(received) != 1 || received[0].Text != "partial" {
		t.Fatalf("received = %#v, want one partial delta", received)
	}
}

func TestResilientClientRetriesAfterCompletedToolCallBeforeToolExecution(t *testing.T) {
	primary := &streamingResilientScript{
		resilientScript: resilientScript{errs: []error{transient(), nil}},
		events:          []StreamEvent{{Type: StreamToolCallDone, ToolCall: &ToolCall{ID: "call_1", Name: "search"}}},
	}
	client := &ResilientClient{Primary: primary, MaxAttempts: 2, RetryDelay: time.Nanosecond}

	if _, err := client.Generate(context.Background(), Request{Model: "primary"}, func(StreamEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 2 {
		t.Fatalf("calls = %d, want 2", primary.calls)
	}
}

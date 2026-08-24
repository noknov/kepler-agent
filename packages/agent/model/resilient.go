package model

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Attempt describes a decision by the shared resilience policy. Runtime
// adapters can persist this through an observer without coupling model code to
// a particular transport or transcript implementation.
type Attempt struct {
	Provider, Model string
	Number          int
	Fallback        bool
	Remaining       time.Duration
	Outcome         string // requested, failed, retrying, fallback, circuit_open, budget_exhausted, completed
	Error           error
}

type attemptObserverKey struct{}

func WithAttemptObserver(ctx context.Context, observe func(Attempt)) context.Context {
	return context.WithValue(ctx, attemptObserverKey{}, observe)
}

func observeAttempt(ctx context.Context, attempt Attempt) {
	if observe, ok := ctx.Value(attemptObserverKey{}).(func(Attempt)); ok && observe != nil {
		observe(attempt)
	}
}

// ResilientClient is the shared policy boundary for model retries, failover,
// and provider/model circuit breaking. Only typed transient, rate-limited, and
// unavailable provider failures may be retried or sent to a fallback.
type ResilientClient struct {
	Primary, Fallback                      Client
	PrimaryProvider, FallbackProvider      string
	FallbackModel                          string
	MaxAttempts, FailureThreshold          int
	Cooldown, RetryDelay, MinAttemptBudget time.Duration
	Now                                    func() time.Time
	Sleep                                  func(context.Context, time.Duration) error

	mu       sync.Mutex
	breakers map[string]breaker
}

type breaker struct {
	failures  int
	openUntil time.Time
}

func (c *ResilientClient) Generate(ctx context.Context, request Request, sink EventSink) (Response, error) {
	if c == nil || c.Primary == nil {
		return Response{}, &Error{Kind: ErrorUnavailable, Message: "primary model provider is not configured"}
	}
	response, err := c.generate(ctx, c.Primary, c.primaryProvider(), request, sink, false)
	if err == nil {
		return response, nil
	}
	if !canFailover(err) || c.Fallback == nil || c.FallbackModel == "" {
		return Response{}, err
	}
	observeAttempt(ctx, c.attempt(ctx, c.fallbackProvider(), c.FallbackModel, 1, true, "fallback", err))
	fallback := request
	fallback.Model = c.FallbackModel
	response, fallbackErr := c.generate(ctx, c.Fallback, c.fallbackProvider(), fallback, sink, true)
	if fallbackErr == nil {
		return response, nil
	}
	if canFailover(fallbackErr) {
		return Response{}, &Error{Kind: ErrorFallbackExhausted, Message: "model fallback chain exhausted", Cause: fallbackErr}
	}
	return Response{}, fallbackErr
}

func (c *ResilientClient) generate(ctx context.Context, client Client, provider string, request Request, sink EventSink, fallback bool) (Response, error) {
	key := provider + "/" + request.Model
	if err := c.allow(key); err != nil {
		observeAttempt(ctx, c.attempt(ctx, provider, request.Model, 0, fallback, "circuit_open", err))
		return Response{}, err
	}
	var last error
	for number := 1; number <= c.maxAttempts(); number++ {
		if err := c.requireBudget(ctx, 0); err != nil {
			observeAttempt(ctx, c.attempt(ctx, provider, request.Model, number, fallback, "budget_exhausted", err))
			return Response{}, err
		}
		observeAttempt(ctx, c.attempt(ctx, provider, request.Model, number, fallback, "requested", nil))
		response, err := client.Generate(ctx, request, sink)
		if err == nil {
			c.succeed(key)
			observeAttempt(ctx, c.attempt(ctx, provider, request.Model, number, fallback, "completed", nil))
			return response, nil
		}
		last = err
		observeAttempt(ctx, c.attempt(ctx, provider, request.Model, number, fallback, "failed", err))
		if !retryable(err) {
			return Response{}, err
		}
		c.fail(key)
		if number == c.maxAttempts() {
			break
		}
		delay := c.retryDelay()
		if err := c.requireBudget(ctx, delay); err != nil {
			observeAttempt(ctx, c.attempt(ctx, provider, request.Model, number, fallback, "budget_exhausted", err))
			return Response{}, err
		}
		observeAttempt(ctx, c.attempt(ctx, provider, request.Model, number, fallback, "retrying", err))
		if err := c.sleep(ctx, delay); err != nil {
			return Response{}, err
		}
	}
	return Response{}, last
}

func (c *ResilientClient) maxAttempts() int {
	if c.MaxAttempts > 0 {
		return c.MaxAttempts
	}
	return 3
}
func (c *ResilientClient) primaryProvider() string {
	if c.PrimaryProvider != "" {
		return c.PrimaryProvider
	}
	return "primary"
}
func (c *ResilientClient) fallbackProvider() string {
	if c.FallbackProvider != "" {
		return c.FallbackProvider
	}
	return "fallback"
}
func (c *ResilientClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
func (c *ResilientClient) retryDelay() time.Duration {
	if c.RetryDelay > 0 {
		return c.RetryDelay
	}
	return 500 * time.Millisecond
}
func (c *ResilientClient) minBudget() time.Duration {
	if c.MinAttemptBudget > 0 {
		return c.MinAttemptBudget
	}
	return 45 * time.Second
}
func (c *ResilientClient) threshold() int {
	if c.FailureThreshold > 0 {
		return c.FailureThreshold
	}
	return 3
}
func (c *ResilientClient) cooldown() time.Duration {
	if c.Cooldown > 0 {
		return c.Cooldown
	}
	return 30 * time.Second
}
func (c *ResilientClient) attempt(ctx context.Context, provider, name string, number int, fallback bool, outcome string, err error) Attempt {
	remaining := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		remaining = deadline.Sub(c.now())
	}
	return Attempt{Provider: provider, Model: name, Number: number, Fallback: fallback, Outcome: outcome, Error: err, Remaining: remaining}
}
func (c *ResilientClient) requireBudget(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	if deadline.Sub(c.now()) >= c.minBudget()+delay {
		return nil
	}
	return &Error{Kind: ErrorBudgetExhausted, Message: "turn execution budget is insufficient for another model request"}
}
func (c *ResilientClient) allow(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.breakers != nil {
		state := c.breakers[key]
		if state.openUntil.After(c.now()) {
			return &Error{Kind: ErrorCircuitOpen, Message: "model provider circuit is open"}
		}
		if !state.openUntil.IsZero() {
			delete(c.breakers, key)
		}
	}
	return nil
}
func (c *ResilientClient) succeed(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.breakers != nil {
		delete(c.breakers, key)
	}
}
func (c *ResilientClient) fail(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.breakers == nil {
		c.breakers = make(map[string]breaker)
	}
	state := c.breakers[key]
	state.failures++
	if state.failures >= c.threshold() {
		state.openUntil = c.now().Add(c.cooldown())
	}
	c.breakers[key] = state
}
func (c *ResilientClient) sleep(ctx context.Context, delay time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var typed *Error
	return errors.As(err, &typed) && typed.Retryable && (typed.Kind == ErrorTransient || typed.Kind == ErrorRateLimited || typed.Kind == ErrorUnavailable)
}
func canFailover(err error) bool { return retryable(err) }

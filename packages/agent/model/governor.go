package model

import (
	"context"
	"fmt"
	"sync"
)

var processGovernor = NewGovernor(16)

type governedClient struct {
	Client
	governor *Governor
}

func Govern(client Client, governor *Governor) Client {
	if client == nil {
		return nil
	}
	if _, ok := client.(*governedClient); ok {
		return client
	}
	if governor == nil {
		governor = processGovernor
	}
	return &governedClient{Client: client, governor: governor}
}

func (c *governedClient) Generate(ctx context.Context, request Request, sink EventSink) (Response, error) {
	ctx = context.WithValue(ctx, admissionGovernorKey{}, c.governor)
	if managed, ok := c.Client.(AttemptManagedClient); ok && managed.ManagesModelAttempts() {
		return c.Client.Generate(ctx, request, sink)
	}
	return DispatchAttempt(ctx, c.Client, request, sink)
}

type budgetKey struct{}

// BudgetState is persisted at reservation and settlement boundaries. On recovery,
// unsettled reservations are conservatively charged because provider usage is unknown.
type BudgetState struct {
	Spent    int64 `json:"spent"`
	Reserved int64 `json:"reserved"`
}

// Budget is inherited through context by every child. Reservations prevent
// simultaneous workers from spending the same remaining token allowance.
type Budget struct {
	mu     sync.Mutex
	limit  int64
	state  BudgetState
	record func(context.Context, BudgetState) error
}

func WithBudget(ctx context.Context, limit int64) context.Context {
	return WithBudgetState(ctx, limit, BudgetState{}, nil)
}

// WithBudgetState installs one ledger for the parent turn. Children inherit it;
// they cannot replace its limit or reset its recovered consumption.
func WithBudgetState(ctx context.Context, limit int64, state BudgetState, record func(context.Context, BudgetState) error) context.Context {
	if limit <= 0 || ctx.Value(budgetKey{}) != nil {
		return ctx
	}
	state.Spent += state.Reserved
	state.Reserved = 0
	return context.WithValue(ctx, budgetKey{}, &Budget{limit: limit, state: state, record: record})
}

func (b *Budget) reserve(ctx context.Context, tokens int64) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if tokens <= 0 || tokens > b.limit-b.state.Spent-b.state.Reserved {
		return &Error{Kind: ErrorBudgetExhausted, Message: "shared parent/worker token budget exhausted"}
	}
	next := b.state
	next.Reserved += tokens
	return b.commit(ctx, next)
}

func (b *Budget) settle(ctx context.Context, reservation int64, usage Usage, err error) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	used := reservation
	if usage.Known() {
		used = UsageTokens(usage)
	}
	next := b.state
	next.Reserved -= reservation
	next.Spent += used
	return b.commit(ctx, next)
}

func (b *Budget) commit(ctx context.Context, next BudgetState) error {
	if b.record != nil {
		if err := b.record(ctx, next); err != nil {
			return fmt.Errorf("persist model token budget: %w", err)
		}
	}
	b.state = next
	return nil
}

// UsageTokens normalizes provider cache accounting before charging an allowance.
func UsageTokens(usage Usage) int64 {
	used := max(0, usage.InputTokens) + max(0, usage.OutputTokens)
	if !usage.CacheTokensIncludedInInput {
		used += max(0, usage.CacheReadTokens) + max(0, usage.CacheCreatedTokens)
	}
	return used
}

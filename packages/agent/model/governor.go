package model

import (
	"context"
	"sync"
)

// Governor bounds actual model work across parents, workers and compactors.
// Sharing it avoids multiplying a per-tool limit by every delegation batch.
type Governor struct{ slots chan struct{} }

func NewGovernor(concurrency int) *Governor {
	if concurrency <= 0 {
		concurrency = 16
	}
	return &Governor{slots: make(chan struct{}, concurrency)}
}

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
	select {
	case c.governor.slots <- struct{}{}:
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
	defer func() { <-c.governor.slots }()
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	budget, _ := ctx.Value(budgetKey{}).(*Budget)
	reservation := int64(request.InputTokenEstimate + request.MaxOutputTokens)
	if err := budget.reserve(reservation); err != nil {
		return Response{}, err
	}
	response, err := c.Client.Generate(ctx, request, sink)
	budget.settle(reservation, response.Usage, err)
	return response, err
}

type budgetKey struct{}

// Budget is inherited through context by every child. Reservations prevent
// simultaneous workers from spending the same remaining token allowance.
type Budget struct {
	mu                     sync.Mutex
	limit, spent, reserved int64
}

func WithBudget(ctx context.Context, limit int64) context.Context {
	if limit <= 0 || ctx.Value(budgetKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, budgetKey{}, &Budget{limit: limit})
}

func (b *Budget) reserve(tokens int64) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if tokens <= 0 || b.spent+b.reserved+tokens > b.limit {
		return &Error{Kind: ErrorBudgetExhausted, Message: "shared parent/worker token budget exhausted"}
	}
	b.reserved += tokens
	return nil
}

func (b *Budget) settle(reservation int64, usage Usage, err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reserved -= reservation
	used := reservation // Unknown/failed requests may have consumed provider tokens.
	if err == nil && usage.Known() {
		used = usage.InputTokens + usage.OutputTokens
		if !usage.CacheTokensIncludedInInput {
			used += usage.CacheReadTokens + usage.CacheCreatedTokens
		}
	}
	b.spent += used
}

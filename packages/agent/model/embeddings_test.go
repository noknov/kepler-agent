package model

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type embedFunc func(context.Context, EmbeddingRequest) (EmbeddingResponse, error)

func (f embedFunc) Embed(ctx context.Context, r EmbeddingRequest) (EmbeddingResponse, error) {
	return f(ctx, r)
}

func TestEmbeddingSharesAllowanceAndAccountsUnknownFailures(t *testing.T) {
	var state BudgetState
	ctx := WithBudgetState(context.Background(), 10, BudgetState{}, func(_ context.Context, next BudgetState) error { state = next; return nil })
	var receipts []AttemptUsage
	ctx = WithUsageRecorder(ctx, func(_ context.Context, receipt AttemptUsage) error { receipts = append(receipts, receipt); return nil })
	calls := 0
	client := GovernEmbedder(embedFunc(func(context.Context, EmbeddingRequest) (EmbeddingResponse, error) {
		calls++
		if calls == 1 {
			return EmbeddingResponse{Vectors: [][]float64{{1}}, Usage: Usage{Reported: true, InputTokens: 4}}, nil
		}
		return EmbeddingResponse{}, errors.New("unknown provider outcome")
	}), NewGovernor(1))
	if _, err := client.Embed(ctx, EmbeddingRequest{Model: "embedding", InputTokenEstimate: 6}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embed(ctx, EmbeddingRequest{Model: "embedding", InputTokenEstimate: 7}); ErrorKindOf(err) != ErrorBudgetExhausted {
		t.Fatalf("allowance bypass: %v", err)
	}
	if _, err := client.Embed(ctx, EmbeddingRequest{Model: "embedding", InputTokenEstimate: 6}); err == nil {
		t.Fatal("provider failure lost")
	}
	if calls != 2 || len(receipts) != 2 || receipts[0].Operation != "embedding" || receipts[0].Charged != 4 || receipts[1].Known || receipts[1].Charged != 6 {
		t.Fatalf("calls=%d receipts=%#v", calls, receipts)
	}
	if state.Spent != 10 || state.Reserved != 0 {
		t.Fatalf("state=%#v", state)
	}
}

func TestEmbeddingAndGenerationShareAdmission(t *testing.T) {
	governor := NewGovernor(1)
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	client := GovernEmbedder(embedFunc(func(ctx context.Context, _ EmbeddingRequest) (EmbeddingResponse, error) {
		close(entered)
		select {
		case <-release:
			return EmbeddingResponse{}, nil
		case <-ctx.Done():
			return EmbeddingResponse{}, ctx.Err()
		}
	}), governor)
	go func() { _, err := client.Embed(context.Background(), EmbeddingRequest{}); finished <- err }()
	<-entered
	raw := &blockingClient{release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := Govern(raw, governor).Generate(ctx, Request{}, nil)
	if err == nil || raw.peak.Load() != 0 {
		t.Fatal("generation bypassed the embedding admission slot")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestNegativeEmbeddingEstimateNeverDispatches(t *testing.T) {
	var calls atomic.Int32
	client := GovernEmbedder(embedFunc(func(context.Context, EmbeddingRequest) (EmbeddingResponse, error) {
		calls.Add(1)
		return EmbeddingResponse{}, nil
	}), NewGovernor(1))
	_, err := client.Embed(context.Background(), EmbeddingRequest{InputTokenEstimate: -1})
	if ErrorKindOf(err) != ErrorInvalid || calls.Load() != 0 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

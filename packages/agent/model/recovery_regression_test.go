package model

import (
	"context"
	"testing"
	"time"
)

func TestCircuitOpenStillUsesHealthyFallback(t *testing.T) {
	primary := &resilientScript{errs: []error{transient()}}
	fallback := &resilientScript{}
	client := &ResilientClient{Primary: primary, Fallback: fallback, FallbackModel: "secondary", MaxAttempts: 1, FailureThreshold: 1, Cooldown: time.Minute}
	_, first := client.Generate(context.Background(), Request{Model: "primary"}, nil)
	_, second := client.Generate(context.Background(), Request{Model: "primary"}, nil)
	t.Logf("first=%v second=%v primary_calls=%d fallback_calls=%d", first, second, primary.calls, fallback.calls)
	if first != nil || second != nil || primary.calls != 1 || fallback.calls != 2 {
		t.Fatal("healthy fallback must remain available while primary circuit is open")
	}
}

func TestCircuitErrorAfterCommittedOutputCannotFailOver(t *testing.T) {
	primary := &streamingResilientScript{
		resilientScript: resilientScript{errs: []error{&Error{Kind: ErrorCircuitOpen, Message: "provider circuit opened"}}},
		events:          []StreamEvent{{Type: StreamTextDelta, Text: "visible"}},
	}
	fallback := &resilientScript{}
	client := &ResilientClient{Primary: primary, Fallback: fallback, FallbackModel: "secondary"}
	_, err := client.Generate(context.Background(), Request{Model: "primary"}, func(StreamEvent) error { return nil })
	if err == nil || fallback.calls != 0 {
		t.Fatalf("error=%v fallback calls=%d", err, fallback.calls)
	}
}

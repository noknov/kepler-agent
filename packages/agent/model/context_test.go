package model

import (
	"context"
	"testing"
)

type identityClient struct {
	key string
	err error
}

func (c identityClient) InputContextKey(context.Context, string) (string, error) { return c.key, nil }
func (c identityClient) Generate(context.Context, Request, EventSink) (Response, error) {
	return Response{InputContextKey: c.key, FinishReason: FinishStop, Usage: Usage{InputTokens: 123}}, c.err
}

func TestContextIdentitySurvivesGovernanceButDoesNotFollowFallback(t *testing.T) {
	client := Govern(&ResilientClient{
		Primary:  identityClient{key: "primary", err: &Error{Kind: ErrorUnavailable, Retryable: true, Message: "fixture unavailable"}},
		Fallback: identityClient{key: "fallback"}, FallbackModel: "fallback-model", MaxAttempts: 1,
	}, NewGovernor(1))
	key, err := InputContextKey(t.Context(), client, "primary-model")
	if err != nil || key != "primary" {
		t.Fatalf("wrapper lost the primary rendering identity: %q %v", key, err)
	}
	response, err := client.Generate(t.Context(), Request{Model: "primary-model"}, nil)
	if err != nil || response.InputContextKey != "fallback" {
		t.Fatalf("fallback overwrote the measured rendering identity: %+v %v", response, err)
	}
	if response.InputContextKey == key {
		t.Fatal("fallback usage could be applied to primary input")
	}
}

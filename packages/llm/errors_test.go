package llm

import (
	"fmt"
	"testing"
)

func TestTemporaryOverloadErrors(t *testing.T) {
	err := ProviderError{Provider: "anthropic messages", StatusCode: 429, Body: "overloaded"}
	if !IsTemporaryOverload(err) {
		t.Fatal("expected 429 provider error to be temporary overload")
	}
	if !IsTemporaryOverload(fmt.Errorf("wrapped: %w", err)) {
		t.Fatal("expected wrapped 429 provider error to be temporary overload")
	}
	if !IsTemporaryOverload(ProviderError{Provider: "opencode-go stream", StatusCode: 522, Body: "error code: 522"}) {
		t.Fatal("expected 522 provider error to be temporary overload")
	}
	if IsTemporaryOverload(ProviderError{Provider: "anthropic messages", StatusCode: 400, Body: "bad request"}) {
		t.Fatal("did not expect 400 provider error to be temporary overload")
	}
	for _, provider := range []string{"opencode-go stream", "opencode-zen chat completion"} {
		if IsTemporaryOverload(ProviderError{Provider: provider, StatusCode: 400, Body: `{"error":{"message":"Invalid schema for function"}}`}) {
			t.Fatalf("invalid schema from %s must not be retried based on provider name", provider)
		}
	}
}

func TestContentPolicyBody(t *testing.T) {
	if !ContentPolicyBody(`{"error":{"message":"Content Exists Risk","type":"invalid_request_error"}}`) {
		t.Fatal("expected DeepSeek content risk body to match")
	}
	if ContentPolicyBody(`{"error":{"message":"Invalid schema for function"}}`) {
		t.Fatal("did not expect an unrelated 400 body to match")
	}
	if ContentPolicyBody("") {
		t.Fatal("did not expect an empty body to match")
	}
}

func TestIsRateLimited(t *testing.T) {
	if !IsRateLimited(ProviderError{Provider: "anthropic", StatusCode: 429, Body: "rate limited"}) {
		t.Fatal("expected 429 to be rate limited")
	}
	if IsRateLimited(ProviderError{Provider: "anthropic", StatusCode: 503, Body: "busy"}) {
		t.Fatal("did not expect 503 to be rate limited")
	}
}

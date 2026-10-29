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

func TestProviderContentPolicy(t *testing.T) {
	if !NewProviderError("test", 400, `{"error":{"message":"Content Exists Risk","type":"invalid_request_error"}}`).ContentPolicy() {
		t.Fatal("expected DeepSeek content risk body to match")
	}
	if NewProviderError("test", 400, `{"error":{"message":"Invalid schema for function"}}`).ContentPolicy() {
		t.Fatal("did not expect an unrelated 400 body to match")
	}
	if NewProviderError("test", 400, "").ContentPolicy() {
		t.Fatal("did not expect an empty body to match")
	}
}

func TestProviderErrorClassificationUsesStructuredRejections(t *testing.T) {
	for _, test := range []struct {
		name, body string
		context    bool
		policy     bool
	}{
		{"context code", `{"error":{"code":"context_length_exceeded"}}`, true, false},
		{"context type", `{"error":{"type":"context_length_exceeded"}}`, true, false},
		{"anthropic context", `{"error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`, true, false},
		{"policy code", `{"error":{"code":"content_filter"}}`, false, true},
		{"policy type", `{"error":{"type":"content_policy_violation"}}`, false, true},
		{"inspection code", `{"error":{"code":"data_inspection_failed"}}`, false, true},
		{"deepseek request ID", `{"error":{"type":"invalid_request_error","code":"invalid_request_error","message":"Content Exists Risk (request_id: req-123)"}}`, false, true},
		{"conflicting validation code", `{"error":{"type":"invalid_request_error","code":"invalid_schema","message":"Content Exists Risk"}}`, false, false},
		{"unstructured text", `Content Exists Risk: maximum context length`, false, false},
		{"malformed JSON", `{"error":{"code":"content_filter"}`, false, false},
		{"HTML", `<html>maximum context length: safety system</html>`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := NewProviderError("test", 400, test.body)
			if err.ContextLimit() != test.context || err.ContentPolicy() != test.policy {
				t.Fatalf("context=%v policy=%v, want context=%v policy=%v", err.ContextLimit(), err.ContentPolicy(), test.context, test.policy)
			}
		})
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

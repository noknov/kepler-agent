package llm

import (
	"fmt"
	"strings"
	"testing"
	"time"
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
}

func TestUserFacingTemporaryOverloadError(t *testing.T) {
	msg := UserFacingError(ProviderError{Provider: "anthropic messages", StatusCode: 503, Body: "busy"})
	if !strings.Contains(msg, "temporarily overloaded") {
		t.Fatalf("UserFacingError() = %q, want friendly overload message", msg)
	}
	if strings.Contains(msg, "status=503") {
		t.Fatalf("UserFacingError() leaked provider status: %q", msg)
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

func TestIsPromptTooLong(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"prompt is too long for this model", true},
		{"maximum context length exceeded", true},
		{"request exceeds token limit", true},
		{"invalid request", false},
	}
	for _, tc := range cases {
		err := ProviderError{Provider: "anthropic", StatusCode: 400, Body: tc.body}
		if got := IsPromptTooLong(err); got != tc.want {
			t.Fatalf("IsPromptTooLong(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestRetryDelayBounded(t *testing.T) {
	for attempt := 0; attempt < 10; attempt++ {
		delay := RetryDelay(attempt)
		if delay <= 0 {
			t.Fatalf("RetryDelay(%d) = %v, want positive duration", attempt, delay)
		}
		if delay > MaxRetryDelay+MaxRetryDelay/4 {
			t.Fatalf("RetryDelay(%d) = %v, exceeds max with jitter", attempt, delay)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("5"); got != 5*time.Second {
		t.Fatalf("parseRetryAfter(seconds) = %v, want 5s", got)
	}
}

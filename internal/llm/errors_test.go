package llm

import (
	"fmt"
	"strings"
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

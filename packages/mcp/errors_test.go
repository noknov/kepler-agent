package mcp

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestCredentialRejected(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		err := HTTPError{Service: "demo", StatusCode: code, Body: "nope"}
		if !CredentialRejected(err) {
			t.Fatalf("code %d should be rejected", code)
		}
		if !CredentialRejected(fmt.Errorf("wrapped: %w", err)) {
			t.Fatalf("wrapped code %d should be rejected", code)
		}
	}
	if CredentialRejected(HTTPError{Service: "demo", StatusCode: http.StatusBadGateway, Body: "down"}) {
		t.Fatal("502 should not be credential rejection")
	}
	if CredentialRejected(errors.New("other")) {
		t.Fatal("unrelated errors should not be credential rejection")
	}
}

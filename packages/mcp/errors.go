package mcp

import (
	"errors"
	"net/http"
)

// HTTPStatus returns the MCP transport status when err is or wraps HTTPError.
func HTTPStatus(err error) (int, bool) {
	var he HTTPError
	if errors.As(err, &he) {
		return he.StatusCode, true
	}
	return 0, false
}

// CredentialRejected reports whether the remote MCP endpoint refused the bearer token.
func CredentialRejected(err error) bool {
	code, ok := HTTPStatus(err)
	if !ok {
		return false
	}
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

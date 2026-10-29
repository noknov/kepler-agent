package llm

import (
	"io"
	"net/http"
)

// readJSONResponse requires EOF within the response budget. A valid JSON prefix
// is not a complete provider response. HTTP failures keep their status-based
// classification even if the diagnostic body exceeds the budget.
func readJSONResponse(response *http.Response, provider string) ([]byte, error) {
	const maxBytes = 8 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, NewProviderError(provider, response.StatusCode, string(data[:min(len(data), maxBytes)]))
	}
	if len(data) > maxBytes {
		return nil, ProtocolError{Provider: provider, Detail: "JSON response body exceeds limit"}
	}
	return data, nil
}

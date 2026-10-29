package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ProviderError struct {
	Provider   string
	StatusCode int
	Body       string
	reason     providerErrorReason
}

// ProtocolError describes a failed provider response without including content,
// tool arguments, or credentials. It is retryable only before output is committed.
type ProtocolError struct {
	Provider string
	Detail   string
}

func (e ProtocolError) Error() string { return e.Provider + " protocol error: " + e.Detail }

type EmptyResponseError struct {
	Provider     string
	StopReason   string
	ContentTypes []string
	PromptTokens int
	OutputTokens int
	Retryable    bool
}

func (e EmptyResponseError) Error() string {
	detail := e.Provider + " returned no text or tool calls"
	if e.StopReason != "" {
		detail += " (stop_reason=" + e.StopReason + ")"
	}
	return detail
}

func (e ProviderError) Error() string {
	return fmt.Sprintf("%s failed: status=%d body=%s", e.Provider, e.StatusCode, e.Body)
}

func (e ProviderError) Retryable() bool {
	return isRetryableStatus(e.StatusCode)
}

func IsTemporaryOverload(err error) bool {
	var providerErr ProviderError
	if errors.As(err, &providerErr) && providerErr.Retryable() {
		return true
	}
	return isNetworkTimeout(err)
}

func IsRateLimited(err error) bool {
	var pe ProviderError
	if !errors.As(err, &pe) {
		return false
	}
	return pe.StatusCode == 429
}

func IsEmptyResponse(err error) bool {
	var emptyErr EmptyResponseError
	return errors.As(err, &emptyErr)
}

func isNetworkTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr interface{ Timeout() bool }
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isRetryableStatus(status int) bool {
	switch status {
	case 429, 500, 502, 503, 504, 522:
		return true
	default:
		return false
	}
}

type providerErrorReason uint8

const (
	providerErrorOther providerErrorReason = iota
	providerErrorContentPolicy
	providerErrorContextLimit
)

// ContentPolicy and ContextLimit expose normalized wire facts, never diagnostic
// substring guesses. HTTP status and retry policy remain owned by the adapter.
func (e ProviderError) ContentPolicy() bool { return e.reason == providerErrorContentPolicy }
func (e ProviderError) ContextLimit() bool  { return e.reason == providerErrorContextLimit }

func classifyProviderError(status int, body string) providerErrorReason {
	if status != 400 && status != 413 {
		return providerErrorOther
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &envelope) != nil {
		return providerErrorOther
	}
	detail := envelope.Error
	for _, code := range []string{detail.Code, detail.Type} {
		switch code {
		case "context_length_exceeded":
			return providerErrorContextLimit
		case "content_filter", "content_policy_violation", "data_inspection_failed":
			return providerErrorContentPolicy
		}
	}
	if detail.Type != "invalid_request_error" || (detail.Code != "" && detail.Code != "invalid_request_error") {
		return providerErrorOther
	}
	// Anthropic uses this documented invalid_request_error message instead of a
	// distinct context code. DeepSeek similarly exposes its safety rejection in
	// error.message, optionally followed by a request ID. Match these wire forms
	// only; schema values, unrelated fields, HTML and prose are not classifiers.
	if detail.Message == "prompt is too long" || strings.HasPrefix(detail.Message, "prompt is too long: ") {
		return providerErrorContextLimit
	}
	if detail.Message == "Content Exists Risk" || (strings.HasPrefix(detail.Message, "Content Exists Risk (request_id: ") && strings.HasSuffix(detail.Message, ")")) {
		return providerErrorContentPolicy
	}
	return providerErrorOther
}

func NewProviderError(provider string, statusCode int, body string) ProviderError {
	return ProviderError{
		Provider: provider, StatusCode: statusCode,
		Body: compactBody([]byte(body)), reason: classifyProviderError(statusCode, body),
	}
}

func compactBody(data []byte) string {
	body := strings.TrimSpace(string(data))
	lower := strings.ToLower(body)
	if strings.Contains(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") {
		return "html error page (upstream is not the JSON API)"
	}
	if len(body) > 2000 {
		return body[:2000] + "...[truncated]"
	}
	return body
}

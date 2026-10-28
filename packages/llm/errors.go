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
}

// ResponsesStreamError preserves structured failure details after a successful
// HTTP handshake. The HTTP 200 is not an error status; callers classify Code
// rather than guessing a status or parsing diagnostic Message text.
type ResponsesStreamError struct {
	Provider         string `json:"-"`
	Event            string `json:"event"`
	Code             string `json:"code,omitempty"`
	Type             string `json:"type,omitempty"`
	Message          string `json:"message,omitempty"`
	Param            string `json:"param,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
	IncompleteReason string `json:"incomplete_reason,omitempty"`
}

func (e ResponsesStreamError) Error() string {
	details, _ := json.Marshal(e)
	return e.Provider + " responses stream failed: " + string(details)
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

// ContentPolicyBody reports whether an upstream error body is a content safety
// rejection rather than a malformed request. It inspects structured JSON error
// codes/types first and falls back to known provider safety phrases.
func ContentPolicyBody(body string) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return false
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(trimmed), &envelope) == nil {
		code := strings.ToLower(strings.TrimSpace(envelope.Error.Code))
		errType := strings.ToLower(strings.TrimSpace(envelope.Error.Type))
		if code == "content_filter" || code == "content_policy_violation" || code == "data_inspection_failed" ||
			errType == "content_policy_violation" || errType == "content_filter" {
			return true
		}
	}
	lower := strings.ToLower(trimmed)
	for _, phrase := range []string{
		"content exists risk",
		"content_policy_violation",
		"content management policy",
		"safety system",
		"inappropriate content",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// ContextLimitBody reports whether an upstream error indicates that the prompt
// or context window exceeded the model's maximum token capacity.
func ContextLimitBody(body string) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return false
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(trimmed), &envelope) == nil {
		code := strings.ToLower(strings.TrimSpace(envelope.Error.Code))
		errType := strings.ToLower(strings.TrimSpace(envelope.Error.Type))
		if code == "context_length_exceeded" || code == "string_above_max_length" ||
			errType == "context_length_exceeded" {
			return true
		}
	}
	lower := strings.ToLower(trimmed)
	for _, phrase := range []string{
		"context_length_exceeded",
		"maximum context length",
		"prompt is too long",
		"input is too long",
		"exceeds the model's maximum context",
		"request_too_large",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func NewProviderError(provider string, statusCode int, body string) ProviderError {
	return ProviderError{
		Provider:   provider,
		StatusCode: statusCode,
		Body:       body,
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

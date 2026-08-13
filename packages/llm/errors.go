package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type ProviderError struct {
	Provider   string
	StatusCode int
	Body       string
}

type EmptyResponseError struct {
	Provider     string
	StopReason   string
	ContentTypes []string
	PromptTokens int
	OutputTokens int
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
	if isRetryableStatus(e.StatusCode) {
		return true
	}
	// Aggregator gateways proxy requests to arbitrary upstream models and are
	// known to surface upstream failures as 400 instead of 429/5xx. A 400
	// from one of these is treated as transient rather than a genuine client
	// error; a 400 from a direct provider (e.g. Anthropic, OpenAI) is not.
	return e.StatusCode == 400 && isAggregatorGateway(e.Provider)
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

func UserFacingError(err error) string {
	if IsTemporaryOverload(err) {
		return "The service is temporarily overloaded. I've already retried but it didn't go through — please try again in a moment."
	}
	return "Error: " + err.Error()
}

func isRetryableStatus(status int) bool {
	switch status {
	case 429, 500, 502, 503, 504, 522:
		return true
	default:
		return false
	}
}

// aggregatorGateways proxy requests to arbitrary upstream models rather than
// serving a single model themselves. ProviderError.Provider is prefixed with
// one of these names (e.g. "opencode-go stream") for every error the gateway
// produces.
var aggregatorGateways = []string{"opencode-go", "opencode-zen"}

func isAggregatorGateway(provider string) bool {
	for _, name := range aggregatorGateways {
		if strings.HasPrefix(provider, name) {
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
	if len(body) > 2000 {
		return body[:2000] + "...[truncated]"
	}
	return body
}

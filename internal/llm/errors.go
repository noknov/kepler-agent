package llm

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

const (
	MaxRetries     = 5
	BaseRetryDelay = 500 * time.Millisecond
	MaxRetryDelay  = 30 * time.Second
)

type ProviderError struct {
	Provider   string
	StatusCode int
	Body       string
	RetryAfter time.Duration // parsed from retry-after header
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

func IsPromptTooLong(err error) bool {
	var pe ProviderError
	if !errors.As(err, &pe) {
		return false
	}
	body := strings.ToLower(pe.Body)
	return pe.StatusCode == 400 && (strings.Contains(body, "prompt is too long") ||
		strings.Contains(body, "maximum context length") ||
		strings.Contains(body, "token limit"))
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

func RetryDelay(attempt int) time.Duration {
	delay := BaseRetryDelay * time.Duration(1<<uint(attempt))
	if delay > MaxRetryDelay {
		delay = MaxRetryDelay
	}
	// Add jitter: ±25%
	jitter := time.Duration(rand.Int63n(int64(delay)/2)) - delay/4
	return delay + jitter
}

func retryDelay(attempt int) time.Duration {
	return RetryDelay(attempt)
}

func parseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if t, err := httpTimeParse(header); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}

func httpTimeParse(value string) (time.Time, error) {
	return time.Parse(time.RFC1123, value)
}

func NewProviderError(provider string, statusCode int, body string, retryAfterHeader string) ProviderError {
	return ProviderError{
		Provider:   provider,
		StatusCode: statusCode,
		Body:       body,
		RetryAfter: parseRetryAfter(retryAfterHeader),
	}
}

func sleepBeforeRetry(ctx context.Context, attempt int, lastErr error) error {
	delay := retryDelay(attempt)
	var pe ProviderError
	if errors.As(lastErr, &pe) && pe.RetryAfter > 0 {
		delay = pe.RetryAfter
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func compactBody(data []byte) string {
	body := strings.TrimSpace(string(data))
	if len(body) > 2000 {
		return body[:2000] + "...[truncated]"
	}
	return body
}

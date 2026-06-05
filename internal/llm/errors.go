package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ProviderError struct {
	Provider   string
	StatusCode int
	Body       string
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
	case 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

func retryDelay(attempt int) time.Duration {
	switch attempt {
	case 0:
		return 1 * time.Second
	case 1:
		return 2 * time.Second
	default:
		return 4 * time.Second
	}
}

func sleepBeforeRetry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(retryDelay(attempt))
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

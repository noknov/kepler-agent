package slack

import (
	"fmt"
	"strings"
)

// checkedPaginationCursor enforces progression before a page contributes results.
// Empty pages can still advance; only Slack's empty next_cursor means completion.
func checkedPaginationCursor(method, raw string, seen map[string]bool) (string, error) {
	next := strings.TrimSpace(raw)
	if next != "" {
		if seen[next] {
			return "", fmt.Errorf("Slack %s returned a repeated pagination cursor", method)
		}
		seen[next] = true
	}
	return next, nil
}

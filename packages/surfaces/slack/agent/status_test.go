package slackagent

import (
	"context"
	"fmt"
	"testing"
)

type codedSlackStatusError string

func (e codedSlackStatusError) Error() string          { return "sensitive upstream detail" }
func (e codedSlackStatusError) SlackErrorCode() string { return string(e) }

func TestSafeSlackErrorCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "coded", err: fmt.Errorf("wrapped: %w", codedSlackStatusError("missing_scope")), want: "missing_scope"},
		{name: "deadline", err: context.DeadlineExceeded, want: "deadline_exceeded"},
		{name: "unknown", err: fmt.Errorf("secret body"), want: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := safeSlackErrorCode(test.err); got != test.want {
				t.Fatalf("safeSlackErrorCode() = %q, want %q", got, test.want)
			}
		})
	}
}

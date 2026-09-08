package failure

import (
	"context"
	"fmt"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

func TestPublicMessageDoesNotExposeErrorDetails(t *testing.T) {
	err := testError(`deepseek stream failed: status=400 body={"error":{"message":"Invalid schema for function 'workspace-list_repos'"}}`)
	if got := PublicMessage(err); got != ServiceUnavailableMessage {
		t.Fatalf("PublicMessage() = %q, want %q", got, ServiceUnavailableMessage)
	}
}

func TestPublicMessageIdentifiesSafeRecoveryCategories(t *testing.T) {
	protocol := &model.Error{Kind: model.ErrorProtocol, Message: "contains internal detail", Retryable: true}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "timeout", err: context.DeadlineExceeded, want: RequestTimedOutMessage},
		{name: "wrapped protocol", err: fmt.Errorf("fallback: %w", protocol), want: MalformedModelMessage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PublicMessage(test.err); got != test.want {
				t.Fatalf("PublicMessage() = %q, want %q", got, test.want)
			}
		})
	}
}

type testError string

func (e testError) Error() string { return string(e) }

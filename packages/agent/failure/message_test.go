package failure

import "testing"

func TestPublicMessageDoesNotExposeErrorDetails(t *testing.T) {
	err := testError(`deepseek stream failed: status=400 body={"error":{"message":"Invalid schema for function 'workspace-list_repos'"}}`)
	if got := PublicMessage(err); got != ServiceUnavailableMessage {
		t.Fatalf("PublicMessage() = %q, want %q", got, ServiceUnavailableMessage)
	}
}

type testError string

func (e testError) Error() string { return string(e) }

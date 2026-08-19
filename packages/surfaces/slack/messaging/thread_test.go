package messaging

import (
	"context"
	"errors"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

func TestThreadLoaderUsesUserToken(t *testing.T) {
	loader := ThreadLoader{
		UserToken: func(_ context.Context, userID string) (string, error) {
			if userID == "U1" {
				return "xoxp-user", nil
			}
			return "", nil
		},
	}
	got := loader.client(context.Background(), "U1")
	if got == nil {
		t.Fatal("expected user token client when connected")
	}
}

func TestThreadLoaderSkipsWithoutUserToken(t *testing.T) {
	loader := ThreadLoader{
		UserToken: func(context.Context, string) (string, error) {
			return "", errors.New("not connected")
		},
	}
	if got := loader.client(context.Background(), "U1"); got != nil {
		t.Fatal("expected nil client when user is not connected")
	}
}

func TestThreadLoaderSkipsWhenResolverMissing(t *testing.T) {
	loader := ThreadLoader{}
	if got := loader.client(context.Background(), "U1"); got != nil {
		t.Fatal("expected nil client when UserToken is not configured")
	}
}

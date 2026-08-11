package slackhome

import (
	"context"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/config"
)

type stubPublisher struct {
	userID string
	view   map[string]any
	calls  int
}

func (s *stubPublisher) PublishHome(_ context.Context, userID string, view map[string]any) error {
	s.userID = userID
	s.view = view
	s.calls++
	return nil
}

func TestControllerRequestRefreshFallsBackToPublish(t *testing.T) {
	pub := &stubPublisher{}
	controller := Controller{
		Cfg:   config.Config{},
		Slack: pub,
	}

	if err := controller.RequestRefresh(context.Background(), "U123"); err != nil {
		t.Fatalf("RequestRefresh() error = %v", err)
	}
	if pub.calls != 1 {
		t.Fatalf("PublishHome calls = %d, want 1", pub.calls)
	}
	if pub.userID != "U123" {
		t.Fatalf("userID = %q, want U123", pub.userID)
	}
}

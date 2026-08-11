package slackagent

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/runtime"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/profiles/hosted"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
)

func TestIntegrationCrossWorkerSteerQueueAndCancel(t *testing.T) {
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL is not set")
	}
	client, err := redisclient.New(url)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sessionID := "integration-control-" + time.Now().UTC().Format("20060102150405.000000000")
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	runCtx, cancelRun := context.WithCancel(context.Background())
	active := &activeRun{userID: "U1", cancel: cancelRun, steering: &runtime.InputBuffer{}}
	owner := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	owner.Redis, owner.PodID = client, "owner"
	owner.Queue = RedisQueue{Client: client, TTL: time.Minute}
	owner.register(sessionID, active)
	defer owner.unregister(sessionID, active)
	go owner.StartControlSubscriber(ctx)

	peer := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	peer.Redis, peer.PodID = client, "peer"
	peer.Queue = RedisQueue{Client: client, TTL: time.Minute}

	steer := slackconversation.Request{EventID: "steer", UserID: "U1", ThreadTS: "T", Text: "look at redis"}
	eventuallyControl(t, func() bool { return peer.controlActive(sessionID, steer) })
	eventually(t, func() bool { return len(active.steering.Drain()) == 1 })

	peer.ModeForUser = func(string) ConversationMode { return ModeQueue }
	queued := slackconversation.Request{EventID: "queue", UserID: "U1", ThreadTS: "T", Text: "then inspect postgres"}
	if !peer.controlActive(sessionID, queued) {
		t.Fatal("cross-worker queue was not accepted")
	}
	items := owner.drainQueue(sessionID, active)
	if len(items) != 1 || items[0].EventID != "queue" {
		t.Fatalf("queued=%+v", items)
	}

	cancel := slackconversation.Request{EventID: "cancel", UserID: "U1", ThreadTS: "T", Text: "cancel"}
	eventuallyControl(t, func() bool { return peer.controlActive(sessionID, cancel) })
	eventually(t, func() bool { return runCtx.Err() == context.Canceled })
}

func eventuallyControl(t *testing.T, fn func() bool) {
	t.Helper()
	eventually(t, fn)
}

func eventually(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met")
}

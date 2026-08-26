package slackagent

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/kepler-agent/packages/infra/redisclient"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

func TestIntegrationCrossWorkerSteerAndQueue(t *testing.T) {
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL is not set")
	}
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	client, err := redisclient.New(url)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	inputs := sessioninput.NewPGStore(pool)
	sessionID := "integration-control-" + time.Now().UTC().Format("20060102150405.000000000")
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_session_inputs WHERE session_id=$1`, sessionID)
	}()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	active := &activeRun{userID: "U1", cancel: func() {}, steering: &steeringInput{store: inputs, sessionID: sessionID, owner: "owner"}}
	owner := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	owner.Redis, owner.PodID, owner.Inputs = client, "owner", inputs
	owner.register(sessionID, active)
	defer owner.unregister(sessionID, active)
	go owner.StartControlSubscriber(ctx)

	peer := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	peer.Redis, peer.PodID, peer.Inputs = client, "peer", inputs

	steer := slackconversation.Request{EventID: "steer", UserID: "U1", ThreadTS: "T", Text: "look at redis"}
	eventuallyControl(t, func() bool { ok, err := peer.controlActive(sessionID, steer); return err == nil && ok })
	eventually(t, func() bool { inputs, _ := active.steering.Claim(context.Background(), 10); return len(inputs) == 1 })

	peer.ModeForUser = func(string) ConversationMode { return ModeQueue }
	queued := slackconversation.Request{EventID: "queue", UserID: "U1", ThreadTS: "T", Text: "then inspect postgres"}
	if ok, err := peer.controlActive(sessionID, queued); err != nil || !ok {
		t.Fatal("cross-worker queue was not accepted")
	}
	items, err := inputs.Claim(context.Background(), sessionID, sessioninput.KindQueue, "owner", time.Minute, 1)
	if err != nil || len(items) != 1 || items[0].ID != "queue" {
		t.Fatalf("queued=%+v err=%v", items, err)
	}

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

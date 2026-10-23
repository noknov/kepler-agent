package slackagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/infra/redisclient"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/session"
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
	canceled := make(chan struct{})
	var once sync.Once
	active := &activeRun{userID: "U1", turnID: "active-turn", cancel: func() { once.Do(func() { close(canceled) }) }, steering: &steeringInput{store: inputs, sessionID: sessionID, owner: "owner"}}
	owner := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	owner.Redis, owner.PodID, owner.Inputs = client, "owner", inputs
	owner.register(sessionID, active)
	defer owner.unregister(sessionID, active)

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
	// A second worker can have registered a request that is still waiting for
	// the session lock. Its Redis hint must not hide the actual running turn.
	store := transcript.NewMemoryStore()
	_, _ = store.Append(ctx, transcript.Event{SessionID: sessionID, Type: transcript.TurnStarted, TurnID: "active-turn", Timestamp: time.Now()})
	catalog, err := tool.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{Model: &replyModel{}, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	peer.Agent.Runtime = runner
	waitingCtx, waitingCancel := context.WithCancel(ctx)
	defer waitingCancel()
	waiter := &activeRun{turnID: "waiting-turn", cancel: waitingCancel}
	peer.register(sessionID, waiter)
	defer peer.unregister(sessionID, waiter)

	command, _ := json.Marshal(stopCommand{TurnID: "active-turn"})
	if err := inputs.Enqueue(ctx, sessioninput.Item{ID: sessionID + "-stop", SessionID: sessionID, Kind: sessioninput.KindStop, Payload: command}); err != nil {
		t.Fatal(err)
	}
	if err := peer.consumeStops(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_session_inputs WHERE id=$1 AND acknowledged_at IS NULL`, sessionID+"-stop").Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("waiter consumed another worker's stop: pending=%d err=%v", pending, err)
	}
	// First establish that the waiter alone cannot claim the owner's stop.
	// Only then start delivery: an earlier control wake-up could otherwise let
	// the real owner acknowledge the command before the pending-row assertion.
	var subscribers sync.WaitGroup
	defer func() { stop(); subscribers.Wait() }()
	for _, service := range []*Service{owner, peer} {
		subscribers.Add(1)
		go func(service *Service) {
			defer subscribers.Done()
			service.StartControlSubscriber(ctx)
		}(service)
	}
	eventually(t, func() bool {
		listeners, err := peer.Redis.PublishCount(ctx, sessionStopChannel, sessionID)
		return err == nil && listeners >= 2
	})
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("owning worker did not consume the durable stop command")
	}
	// Cancellation precedes the durable acknowledgement. Wait for both before
	// closing the pool/context so cleanup cannot race a successful delivery.
	eventually(t, func() bool {
		var acknowledged bool
		err := pool.QueryRow(ctx, `SELECT acknowledged_at IS NOT NULL FROM agent_session_inputs WHERE id=$1`, sessionID+"-stop").Scan(&acknowledged)
		return err == nil && acknowledged
	})
	if waitingCtx.Err() != nil {
		t.Fatal("Stop canceled the later waiting request")
	}

}

func TestIntegrationLocalStopSurvivesRedisFailure(t *testing.T) {
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL is not set")
	}
	client, err := redisclient.New(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close() // fail only this test client's Redis connection
	s := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	s.Redis, s.Inputs = client, &memoryInputs{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessionID := session.ID("C", "T")
	s.active[sessionID] = &activeRun{turnID: "run", cancel: cancel, startedAt: time.Now().Add(-time.Minute)}
	if err := s.StopSession(context.Background(), slackconversation.StopRequest{EventID: "stop", Channel: "C", ThreadTS: "T", EventTS: fmt.Sprintf("%d.000000", time.Now().Unix())}); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("Redis failure prevented local cancellation")
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

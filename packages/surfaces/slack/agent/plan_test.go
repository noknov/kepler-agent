package slackagent

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

func TestPlanCompletionDrainsLatestUpdate(t *testing.T) {
	messenger := &nativeStreamingMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
	stream.UpdatePlan(&tool.PlanUpdate{Items: []tool.PlanItem{{ID: "inspect", Task: "Inspect logs", Status: "in_progress"}}})
	// Wait for initial delivery, then complete inside the throttling window.
	eventually(t, func() bool { messenger.mu.Lock(); defer messenger.mu.Unlock(); return len(messenger.planPosts) == 1 })
	stream.UpdatePlan(&tool.PlanUpdate{Items: []tool.PlanItem{{ID: "inspect", Task: "Inspect logs", Status: "completed"}}})
	if _, err := stream.Complete("Done"); err != nil {
		t.Fatal(err)
	}
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if len(messenger.planUpdates) != 1 {
		t.Fatalf("final plan update lost: %#v", messenger.planUpdates)
	}
	tasks := messenger.planUpdates[0][0]["tasks"].([]map[string]any)
	if tasks[0]["status"] != "complete" {
		t.Fatalf("tasks = %#v", tasks)
	}
}

type blockedPlanMessenger struct {
	nativeStreamingMessenger
	entered, release chan struct{}
}

func (m *blockedPlanMessenger) PostMessageBlocks(ctx context.Context, channel, thread, text string, blocks []map[string]any) (string, error) {
	close(m.entered)
	select {
	case <-m.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return m.nativeStreamingMessenger.PostMessageBlocks(ctx, channel, thread, text, blocks)
}

func TestPlanDeliveryDoesNotBlockRuntime(t *testing.T) {
	messenger := &blockedPlanMessenger{entered: make(chan struct{}), release: make(chan struct{})}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
	released := false
	defer func() {
		if !released {
			close(messenger.release)
		}
		stream.Complete("Done")
	}()
	returned := make(chan struct{})
	go func() {
		stream.UpdatePlan(&tool.PlanUpdate{Items: []tool.PlanItem{{ID: "inspect", Task: "Inspect logs", Status: "in_progress"}}})
		close(returned)
	}()
	select {
	case <-messenger.entered:
	case <-time.After(time.Second):
		t.Fatal("plan delivery not started")
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("runtime blocked on Slack plan delivery")
	}
	for range 100 {
		stream.UpdatePlan(&tool.PlanUpdate{Items: []tool.PlanItem{{ID: "inspect", Task: "Inspect logs", Status: "completed"}}})
	}
	close(messenger.release)
	released = true
	if _, err := stream.Complete("Done"); err != nil {
		t.Fatal(err)
	}
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if len(messenger.planPosts) != 1 || len(messenger.planUpdates) != 1 {
		t.Fatalf("plan updates did not coalesce: posts=%d updates=%d", len(messenger.planPosts), len(messenger.planUpdates))
	}
}

func TestPlanFailureDrainsSnapshotAndRejectsLateUpdates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		messenger := &nativeStreamingMessenger{}
		stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
		plan := &tool.PlanUpdate{Items: []tool.PlanItem{{ID: "inspect", Task: "Inspect logs", Status: "in_progress"}}}
		stream.UpdatePlan(plan)
		synctest.Wait()
		plan.Items[0].Status = "blocked"
		stream.UpdatePlan(plan)
		plan.Items[0].Status = "completed" // The pending snapshot must not alias this.
		if _, err := stream.Fail("", true); err != nil {
			t.Fatal(err)
		}
		stream.UpdatePlan(plan) // Delayed event after the terminal barrier.
		time.Sleep(2 * planUpdateInterval)
		synctest.Wait()
		messenger.mu.Lock()
		defer messenger.mu.Unlock()
		if len(messenger.planPosts) != 1 || len(messenger.planUpdates) != 1 {
			t.Fatalf("posts=%d updates=%d", len(messenger.planPosts), len(messenger.planUpdates))
		}
		tasks := messenger.planUpdates[0][0]["tasks"].([]map[string]any)
		if tasks[0]["status"] != "error" {
			t.Fatalf("last authored plan replaced: %#v", tasks)
		}
	})
}

type uncertainPlanMessenger struct {
	nativeStreamingMessenger
	attempts int
}

func (m *uncertainPlanMessenger) PostMessageBlocks(context.Context, string, string, string, []map[string]any) (string, error) {
	m.attempts++
	return "", uncertainStartError{}
}

func TestPlanDoesNotRepostAfterUnknownDelivery(t *testing.T) {
	messenger := &uncertainPlanMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
	stream.UpdatePlan(&tool.PlanUpdate{Items: []tool.PlanItem{{ID: "inspect", Task: "Inspect", Status: "in_progress"}}})
	stream.flushDeferredPlan(false)
	stream.UpdatePlan(&tool.PlanUpdate{Items: []tool.PlanItem{{ID: "inspect", Task: "Inspect", Status: "completed"}}})
	stream.finishPlan()
	if messenger.attempts != 1 {
		t.Fatalf("unknown plan delivery was repeated %d times", messenger.attempts)
	}
}

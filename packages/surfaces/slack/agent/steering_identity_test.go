package slackagent

import (
	"context"
	"encoding/json"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	"testing"
	"time"
)

func TestSteeringConsumerSeparatesActors(t *testing.T) {
	ctx := context.Background()
	store := &memoryInputs{}
	input := &steeringInput{store: store, sessionID: "session", owner: "worker-A", userID: "A"}
	for _, req := range []slackconversation.Request{{EventID: "B", UserID: "B", Text: "other actor"}, {EventID: "A", UserID: "A", Text: "same actor"}} {
		if err := input.Push(req); err != nil {
			t.Fatal(err)
		}
	}
	got, err := input.Claim(ctx, 10)
	if err != nil || len(got) != 1 || got[0].ID != "A" {
		t.Fatalf("inputs=%+v err=%v", got, err)
	}
	queued, err := store.Claim(ctx, "session", sessioninput.KindQueue, "worker-B", time.Minute, 1)
	if err != nil || len(queued) != 1 {
		t.Fatalf("queue=%+v err=%v", queued, err)
	}
	var req slackconversation.Request
	if err := json.Unmarshal(queued[0].Payload, &req); err != nil {
		t.Fatal(err)
	}
	if req.UserID != "B" || req.Text != "other actor" {
		t.Fatalf("lost actor/input: %+v", req)
	}
}

func TestLocalSteeringQueuesOtherActor(t *testing.T) {
	store := &memoryInputs{}
	service := New(hosted.Agent{}, nil, safety.PromptPolicy{}, safety.Redactor{}, nil)
	service.Inputs = store
	service.active["session"] = &activeRun{userID: "A"}
	ok, err := service.controlActive("session", slackconversation.Request{EventID: "B", UserID: "B", Text: "follow up"})
	if err != nil || !ok {
		t.Fatalf("accepted=%v err=%v", ok, err)
	}
	items, err := store.Claim(context.Background(), "session", sessioninput.KindQueue, "worker", time.Minute, 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("queue=%+v err=%v", items, err)
	}
}

func TestSteeringRetainsItsOwnViewingSnapshot(t *testing.T) {
	store := &memoryInputs{}
	input := &steeringInput{store: store, sessionID: "session", owner: "worker", userID: "U1"}
	req := slackconversation.Request{TeamID: "T1", EventID: "context-update", UserID: "U1", Channel: "D1", Text: "use this channel instead", ViewingContext: &slackconversation.ViewingContext{TeamID: "T1", ChannelID: "C456"}}
	if err := input.Push(req); err != nil {
		t.Fatal(err)
	}
	items, err := input.Claim(context.Background(), 1)
	if err != nil || len(items) != 1 || len(items[0].Message.Content) != 2 || items[0].Message.Text() != req.Text {
		t.Fatalf("lost steering context: %+v %v", items, err)
	}
}

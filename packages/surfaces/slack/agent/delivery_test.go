package slackagent

import (
	"context"
	"errors"
	"sync"
	"testing"

	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type memoryDelivery struct {
	mu           sync.Mutex
	receipt      ReplyReceipt
	advanceError error
}

func (s *memoryDelivery) Begin(_ context.Context, req ReplyReceipt) (ReplyReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.receipt
	r.TurnID = req.TurnID
	r.SessionID = req.SessionID
	r.Channel = req.Channel
	r.ThreadTS = req.ThreadTS
	r.UserID = req.UserID
	s.receipt = r
	return r, nil
}
func (s *memoryDelivery) Advance(_ context.Context, r ReplyReceipt, phase, ts string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.advanceError != nil {
		return s.advanceError
	}
	if r.Phase != s.receipt.Phase || !validReplyTransition(r.Phase, phase) {
		return errors.New("invalid fixture receipt transition")
	}
	s.receipt.Phase = phase
	s.receipt.MessageTS = ts
	return nil
}
func (s *memoryDelivery) Status(_ context.Context, _ ReplyReceipt, status string, applied bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.receipt.DesiredStatus = status
	s.receipt.StatusApplied = applied
	return nil
}
func (s *memoryDelivery) Pending(context.Context, int) ([]ReplyReceipt, error) { return nil, nil }

func TestRestartRepairsKnownReplyAndDoesNotRepeatUnreceiptedCreation(t *testing.T) {
	for _, phase := range []string{"streaming", "starting", "posting"} {
		t.Run(phase, func(t *testing.T) {
			messenger := &nativeStreamingMessenger{}
			store := &memoryDelivery{receipt: ReplyReceipt{Phase: phase}}
			if phase == "streaming" {
				store.receipt.MessageTS = "known.123"
			}
			s := New(hosted.Agent{}, messenger, safety.PromptPolicy{}, safety.Redactor{}, nil)
			s.Deliveries = store
			catalog, _ := tool.NewCatalog()
			runner, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: &replyModel{}, Tools: catalog, Transcript: transcript.NewMemoryStore(), Events: s.EventSink()})
			if err != nil {
				t.Fatal(err)
			}
			s.Agent.Runtime = runner
			if _, err := s.handle(t.Context(), conversation.Request{EventID: "turn", UserID: "U", Channel: "C", ThreadTS: "T", Text: "continue"}); err != nil {
				t.Fatal(err)
			}
			messenger.mu.Lock()
			defer messenger.mu.Unlock()
			if messenger.started != 0 || len(messenger.posts) != 0 {
				t.Fatalf("recovery created duplicate reply: starts=%d posts=%v", messenger.started, messenger.posts)
			}
			if phase == "streaming" {
				if len(messenger.updates) != 1 || messenger.updates[0] != "hello" || store.receipt.Phase != "delivered" {
					t.Fatalf("known message was not repaired: updates=%v receipt=%+v", messenger.updates, store.receipt)
				}
			} else if len(messenger.updates) != 0 || store.receipt.Phase != "uncertain" {
				t.Fatalf("unknown receipt was repeated: %+v", store.receipt)
			}
			if !store.receipt.StatusApplied || store.receipt.DesiredStatus != sessionActive {
				t.Fatalf("terminal session was not settled: %+v", store.receipt)
			}
		})
	}
}

func TestReplyCreationRequiresPersistedIntent(t *testing.T) {
	messenger := &nativeStreamingMessenger{}
	store := &memoryDelivery{receipt: ReplyReceipt{Phase: "prepared"}, advanceError: errors.New("fixture disk unavailable")}
	stream := newSlackStream(t.Context(), messenger, conversation.Request{EventID: "turn", Channel: "C", ThreadTS: "T"})
	stream.delivery = &replyDelivery{store: store, receipt: store.receipt}
	stream.AppendDelta("answer")
	_, _ = stream.Complete("answer")
	if stream.delivery.persistenceError() == nil {
		t.Fatal("intent failure was lost")
	}
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if messenger.started != 0 || len(messenger.posts) != 0 {
		t.Fatal("network create ran without durable intent")
	}
}

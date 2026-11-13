package slackagent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type admissionModel struct {
	started chan struct{}
	calls   atomic.Int32
}

func (m *admissionModel) Generate(ctx context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	if m.calls.Add(1) == 1 {
		close(m.started)
	}
	<-ctx.Done()
	return model.Response{}, ctx.Err()
}

func TestFollowupAdmissionDoesNotWaitForExecutionCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	m := &admissionModel{started: make(chan struct{})}
	catalog, _ := tool.NewCatalog()
	runtime, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: m, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	s := New(hosted.Agent{Runtime: runtime}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	inputs := &memoryInputs{}
	s.Inputs = inputs
	s.MaxConcurrentRuns = 1
	done := make(chan struct{})
	go func() { s.StartControlSubscriber(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("dispatcher did not join canceled execution")
		}
	})
	request := conversation.Request{EventID: "initial", UserID: "U1", Channel: "C1", ThreadTS: "T1", Text: "long task"}
	if accepted, err := s.HandleMention(ctx, request); err != nil || !accepted {
		t.Fatalf("accepted=%v error=%v", accepted, err)
	}
	select {
	case <-m.started:
	case <-time.After(time.Second):
		t.Fatal("initial execution did not start")
	}
	admitted := make(chan error, 1)
	go func() {
		request.EventID = "followup"
		request.Text = "change the scope"
		_, err := s.HandleMention(ctx, request)
		admitted <- err
	}()
	select {
	case err := <-admitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("followup blocked behind long execution")
	}
	inputs.mu.Lock()
	var found bool
	for _, item := range inputs.items {
		if item.ID == "followup" && item.Kind == sessioninput.KindSteering {
			found = true
		}
	}
	inputs.mu.Unlock()
	if !found {
		t.Fatal("followup did not reach durable steering")
	}
	request.EventID = "other"
	request.ThreadTS = "T2"
	if _, err := s.HandleMention(ctx, request); err != nil {
		t.Fatal(err)
	}
	if m.calls.Load() != 1 {
		t.Fatal("execution capacity was bypassed")
	}
}

type failedAdmissionStore struct{ memoryInputs }

func (*failedAdmissionStore) Enqueue(context.Context, sessioninput.Item) error {
	return errors.New("fixture storage unavailable")
}
func TestAdmissionDoesNotAcknowledgeAnUnpersistedInput(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	runtime, _ := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: &replyModel{}, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	s := New(hosted.Agent{Runtime: runtime}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	s.Inputs = &failedAdmissionStore{}
	accepted, err := s.HandleMention(t.Context(), conversation.Request{EventID: "E", UserID: "U", Channel: "C", ThreadTS: "T", Text: "hello"})
	if accepted || err == nil || len(s.dispatch) != 0 {
		t.Fatalf("accepted=%v error=%v queued=%d", accepted, err, len(s.dispatch))
	}
}

type failedInputReceipts struct {
	memoryInputs
	failures int
}

func (s *failedInputReceipts) Fail(ctx context.Context, id, owner string, cause error, delay time.Duration, attempts int) (bool, error) {
	s.failures++
	return s.memoryInputs.Fail(ctx, id, owner, cause, delay, attempts)
}

func TestMalformedQueuedInputUsesBoundedFailureReceipt(t *testing.T) {
	s := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	inputs := &failedInputReceipts{}
	s.Inputs = inputs
	if err := inputs.Enqueue(t.Context(), sessioninput.Item{ID: "broken", SessionID: "session", Kind: sessioninput.KindQueue, Payload: []byte(`{"UserID":13}`)}); err != nil {
		t.Fatal(err)
	}
	s.startPending(t.Context(), "session")
	if inputs.failures != 1 {
		t.Fatalf("malformed input bypassed retry/dead-letter policy: %d failure receipts", inputs.failures)
	}
}

func TestExecutionConflictReleasesOnlyTheClaimedInput(t *testing.T) {
	s := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	inputs := &memoryInputs{}
	s.Inputs = inputs
	active := &activeRun{userID: "U", turnID: "running", steering: &steeringInput{store: inputs, sessionID: conversation.Target{Channel: "C", ThreadTS: "T"}.SessionID(), owner: s.owner(), userID: "U"}}
	s.active[conversation.Target{Channel: "C", ThreadTS: "T"}.SessionID()] = active
	request := conversation.Request{EventID: "queued", UserID: "U", Channel: "C", ThreadTS: "T"}
	if err := s.persistInput(t.Context(), conversation.Target{Channel: "C", ThreadTS: "T"}.SessionID(), sessioninput.KindQueue, request); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.claimNextQueue(t.Context(), conversation.Target{Channel: "C", ThreadTS: "T"}.SessionID())
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.run(t.Context(), conversation.Target{Channel: "C", ThreadTS: "T"}.SessionID(), *claimed); err != nil {
		t.Fatal(err)
	}
	inputs.mu.Lock()
	defer inputs.mu.Unlock()
	if inputs.claimed[request.EventID] != "" || inputs.acked[request.EventID] || len(inputs.items) != 1 || inputs.items[0].Kind != sessioninput.KindQueue {
		t.Fatal("conflicting execution consumed or re-admitted the queued input")
	}
	if s.active[conversation.Target{Channel: "C", ThreadTS: "T"}.SessionID()] != active {
		t.Fatal("conflicting execution displaced the session owner")
	}
}

func TestAutomaticResumeNeverSteersAnotherActiveTurn(t *testing.T) {
	s, continuation := waitingConnectionService(t)
	id := continuation.SessionID
	s.active[id] = &activeRun{userID: "requester", turnID: "new-active", steering: &steeringInput{store: s.Inputs, sessionID: id, owner: s.owner(), userID: "requester"}}
	s.resumeAfterConnection(t.Context(), continuation)
	inputs := s.Inputs.(*memoryInputs)
	if len(inputs.items) != 1 || inputs.items[0].Kind != sessioninput.KindQueue {
		t.Fatalf("resume was injected into another active turn: %+v", inputs.items)
	}
}

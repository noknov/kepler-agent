package slackagent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/session"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

func TestStopTargetUsesRunningTurnRatherThanLocalWaiter(t *testing.T) {
	clicked := time.Unix(1792120000, 0)
	events := []transcript.Event{{Type: transcript.TurnStarted, TurnID: "running", Timestamp: clicked.Add(-time.Minute)}}
	waiter := &activeRun{turnID: "waiting", startedAt: clicked.Add(-time.Second)}
	if target, stale := stopTarget(events, waiter, clicked); target != "running" || stale {
		t.Fatalf("target=%q stale=%v", target, stale)
	}
	events = append(events, transcript.Event{Type: transcript.TurnCompleted, TurnID: "running"})
	if target, stale := stopTarget(events, waiter, clicked); target != "waiting" || stale {
		t.Fatalf("startup target=%q stale=%v", target, stale)
	}
	events = append(events, transcript.Event{Type: transcript.TurnStarted, TurnID: "new", Timestamp: clicked.Add(time.Second)})
	if _, stale := stopTarget(events, nil, clicked); !stale {
		t.Fatal("delayed stop selected a later turn")
	}
}

func TestSlackStopTimestampPrecision(t *testing.T) {
	got, err := parseSlackEventTime("1792120000.123456")
	if err != nil || got.Unix() != 1792120000 || got.Nanosecond() != 123456000 {
		t.Fatalf("timestamp=%v err=%v", got, err)
	}
	for _, invalid := range []string{"", "NaN", "Inf", "-1.0", "12.1234567890", "12.1e3", "12.+1"} {
		if _, err := parseSlackEventTime(invalid); err == nil {
			t.Fatalf("accepted malformed timestamp %q", invalid)
		}
	}
}

type stoppableModel struct {
	started chan struct{}
	tool    bool
}

func (m *stoppableModel) Generate(ctx context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	if m.tool {
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call", Name: "wait", Arguments: json.RawMessage(`{}`)}}}}, FinishReason: model.FinishToolCalls}, nil
	}
	close(m.started)
	<-ctx.Done()
	return model.Response{}, ctx.Err()
}

type stoppableTool struct{ started chan struct{} }

func (t stoppableTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "wait", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []tool.Effect{tool.EffectRead}}
}
func (t stoppableTool) Execute(ctx context.Context, _ tool.Call) (tool.Result, error) {
	close(t.started)
	<-ctx.Done()
	return tool.Result{}, ctx.Err()
}

func TestNativeStopCancelsModelAndToolThroughRuntime(t *testing.T) {
	for _, duringTool := range []bool{false, true} {
		t.Run(fmt.Sprintf("tool=%v", duringTool), func(t *testing.T) {
			started := make(chan struct{})
			client := &stoppableModel{started: started, tool: duringTool}
			catalog, err := tool.NewCatalog(stoppableTool{started: started})
			if err != nil {
				t.Fatal(err)
			}
			store := transcript.NewMemoryStore()
			messenger := &fakeMessenger{}
			s := New(hosted.Agent{}, messenger, safety.PromptPolicy{}, safety.Redactor{}, nil)
			s.Inputs = &memoryInputs{}
			runner, err := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: store, Events: s.EventSink()})
			if err != nil {
				t.Fatal(err)
			}
			s.Agent.Runtime = runner
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := s.HandleMention(ctx, conversation.Request{EventID: "run", UserID: "U", Channel: "C", ThreadTS: "T", Text: "wait", Locale: "zh-CN"})
				done <- err
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("model/tool did not start")
			}
			now := time.Now()
			if err := s.StopSession(ctx, conversation.StopRequest{EventID: "stop", UserID: "U", Channel: "C", ThreadTS: "T", EventTS: fmt.Sprintf("%d.%09d", now.Unix(), now.Nanosecond())}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("runtime failed to terminate after Stop")
			}
			events, _ := store.Load(ctx, session.ID("C", "T"), 0)
			canceled := false
			for _, event := range events {
				canceled = canceled || event.Type == transcript.TurnCanceled
			}
			if !canceled {
				t.Fatal("cancellation not recorded in canonical transcript")
			}
			messenger.mu.Lock()
			defer messenger.mu.Unlock()
			if len(messenger.posts) != 1 || messenger.posts[0] != "已中止本次请求。" || messenger.statuses[len(messenger.statuses)-1] != sessionActive {
				t.Fatalf("posts=%v statuses=%v", messenger.posts, messenger.statuses)
			}
		})
	}
}

func TestStopClearsLoadingForOrphanedTurn(t *testing.T) {
	store := transcript.NewMemoryStore()
	sessionID := session.ID("C", "T")
	if _, err := store.Append(context.Background(), transcript.Event{SessionID: sessionID, TurnID: "orphan", Type: transcript.TurnStarted, Timestamp: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	catalog, err := tool.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{Model: &replyModel{}, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{}
	s := New(hosted.Agent{Runtime: runner}, messenger, safety.PromptPolicy{}, safety.Redactor{}, nil)
	s.Inputs = &memoryInputs{}
	if err := s.StopSession(context.Background(), conversation.StopRequest{EventID: "stop", Channel: "C", ThreadTS: "T", EventTS: fmt.Sprintf("%d.000000", time.Now().Unix())}); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(messenger.statuses); got != fmt.Sprint([]string{sessionActive}) {
		t.Fatalf("orphaned status=%s", got)
	}
}

func TestNativeStopTargetsTurnAndIgnoresLateDelivery(t *testing.T) {
	s := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	s.Inputs = &memoryInputs{}
	sessionID := session.ID("C", "T")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.active[sessionID] = &activeRun{turnID: "first", cancel: cancel, startedAt: time.Now().Add(-time.Minute)}
	req := conversation.StopRequest{EventID: "stop", Channel: "C", ThreadTS: "T", EventTS: fmt.Sprintf("%d.000000", time.Now().Unix())}
	if err := s.StopSession(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("native Stop did not cancel the running turn")
	}
	next, nextCancel := context.WithCancel(context.Background())
	defer nextCancel()
	s.active[sessionID] = &activeRun{turnID: "next", cancel: nextCancel, startedAt: time.Now().Add(time.Minute)}
	if err := s.StopSession(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// A previously persisted command must also remain tied to its old turn.
	payload, _ := json.Marshal(stopCommand{TurnID: "first"})
	if err := s.Inputs.Enqueue(context.Background(), sessioninput.Item{ID: "replayed", SessionID: sessionID, Kind: sessioninput.KindStop, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := s.consumeStops(context.Background(), sessionID); err != nil {
		t.Fatal(err)
	}
	if next.Err() != nil {
		t.Fatal("old stop canceled the next turn")
	}
	if s.Inputs.(*memoryInputs).acked["replayed"] {
		t.Fatal("a different local waiter acknowledged the running worker's stop")
	}
}

type orderedStatusMessenger struct {
	nativeStreamingMessenger
	operations []string
}

func (m *orderedStatusMessenger) StartStream(ctx context.Context, req conversation.StreamStart) (string, error) {
	m.operations = append(m.operations, "start")
	return m.nativeStreamingMessenger.StartStream(ctx, req)
}
func (m *orderedStatusMessenger) StopStream(context.Context, string, string) error {
	m.operations = append(m.operations, "stop")
	// Reproduce a stopStream failure: the explicit lifecycle call must still
	// happen after this operation, independent of an earlier cached status.
	return codedSlackStatusError("internal_error")
}
func (m *orderedStatusMessenger) SetAgentSessionStatus(_ context.Context, _, _, _, status string) error {
	m.operations = append(m.operations, status)
	return nil
}

func TestTerminalStatusFollowsLateStreamStartAndFailedStop(t *testing.T) {
	for _, terminal := range []string{"completed", "pending_input", "pending_approval"} {
		t.Run(terminal, func(t *testing.T) {
			m := &orderedStatusMessenger{}
			s := newSlackStream(context.Background(), m, conversation.Request{Channel: "C", ThreadTS: "T"})
			s.Start()
			s.appendSanitizedDelta("answer")
			s.Lifecycle(transcript.Event{Type: transcript.TurnCompleted, Status: terminal})
			if _, err := s.Complete("answer"); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprint([]string{sessionProcessing, "start", "stop", sessionStatusForTermination(terminal)})
			if got := fmt.Sprint(m.operations); got != want {
				t.Fatalf("operations=%s want=%s", got, want)
			}
		})
	}
}

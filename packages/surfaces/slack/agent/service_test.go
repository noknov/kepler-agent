package slackagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type replyModel struct{ request *model.Request }

func (m *replyModel) Generate(_ context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	m.request = &request
	_ = sink(model.StreamEvent{Type: model.StreamTextDelta, Text: "hello"})
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "hello"), FinishReason: model.FinishStop}, nil
}

type fakeMessenger struct {
	mu       sync.Mutex
	posts    []string
	statuses []string
}

type memoryInputs struct {
	mu      sync.Mutex
	items   []sessioninput.Item
	claimed map[string]string
	acked   map[string]bool
}

func (s *memoryInputs) Enqueue(_ context.Context, item sessioninput.Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.items {
		if existing.ID == item.ID {
			return nil
		}
	}
	item.Sequence = int64(len(s.items) + 1)
	s.items = append(s.items, item)
	return nil
}
func (s *memoryInputs) Claim(_ context.Context, session string, kind sessioninput.Kind, owner string, _ time.Duration, limit int) ([]sessioninput.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed == nil {
		s.claimed = make(map[string]string)
	}
	var out []sessioninput.Item
	for _, item := range s.items {
		if item.SessionID != session || item.Kind != kind || s.acked[item.ID] || (s.claimed[item.ID] != "" && s.claimed[item.ID] != owner) {
			continue
		}
		s.claimed[item.ID] = owner
		out = append(out, item)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
func (s *memoryInputs) Ack(_ context.Context, id, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed[id] != owner {
		return sessioninput.ErrClaimLost
	}
	if s.acked == nil {
		s.acked = make(map[string]bool)
	}
	s.acked[id] = true
	return nil
}
func (s *memoryInputs) Release(_ context.Context, id, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed[id] != owner {
		return sessioninput.ErrClaimLost
	}
	delete(s.claimed, id)
	return nil
}
func (*memoryInputs) PendingSessions(context.Context, sessioninput.Kind, int) ([]string, error) {
	return nil, nil
}
func (*memoryInputs) PromoteExpiredSteering(context.Context, time.Duration) (int64, error) {
	return 0, nil
}
func (s *memoryInputs) PromoteSteering(_ context.Context, sessionID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var changed int64
	for index := range s.items {
		if s.items[index].SessionID == sessionID && s.items[index].Kind == sessioninput.KindSteering && !s.acked[s.items[index].ID] {
			s.items[index].Kind = sessioninput.KindQueue
			changed++
		}
	}
	return changed, nil
}

func (m *fakeMessenger) PostMessage(_ context.Context, _, _, text string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posts = append(m.posts, text)
	return "post", nil
}
func (m *fakeMessenger) PostMarkdownMessage(_ context.Context, _, _, text string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posts = append(m.posts, text)
	return "post", nil
}
func (m *fakeMessenger) SetAgentSessionStatus(_ context.Context, _, _, _ string, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses = append(m.statuses, status)
	return nil
}

func TestServiceRunsHostedHarnessAndPostsFormattedAnswer(t *testing.T) {
	catalog, err := tool.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{}
	service := New(hosted.Agent{}, messenger, safety.PromptPolicy{}, safety.Redactor{}, nil)
	client := &replyModel{}
	runner, err := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore(), Events: service.EventSink()})
	if err != nil {
		t.Fatal(err)
	}
	service.Agent.Runtime = runner
	accepted, handleErr := service.handle(context.Background(), slackconversation.Request{EventID: "event-1", UserID: "U1", Channel: "C1", ThreadTS: "T1", Text: "question"})
	if handleErr != nil || !accepted {
		t.Fatal("request was not handled")
	}
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if len(messenger.posts) != 1 || messenger.posts[0] != "hello" {
		t.Fatalf("posts = %#v", messenger.posts)
	}
	if client.request == nil || len(client.request.Messages) == 0 || strings.Contains(client.request.Messages[0].Text(), "transient Slack status") || !strings.Contains(client.request.Messages[0].Text(), slackOutputFormatPrompt) {
		t.Fatalf("primary model received a Slack progress instruction: %+v", client.request)
	}
	if got := messenger.statuses; len(got) != 2 || got[0] != sessionProcessing || got[1] != sessionActive {
		t.Fatalf("session statuses = %#v", got)
	}
}

func TestExplicitMentionResumesPendingInput(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	store := transcript.NewMemoryStore()
	messenger := &fakeMessenger{}
	service := New(hosted.Agent{}, messenger, safety.PromptPolicy{}, safety.Redactor{}, nil)
	client := &replyModel{}
	runner, _ := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: store, Events: service.EventSink()})
	service.Agent.Runtime = runner
	sessionID := session.ID("C", "T")
	metadata := json.RawMessage(`{"user_id":"U1"}`)
	_, _ = store.Append(context.Background(), transcript.Event{ID: "start", SessionID: sessionID, TurnID: "first", Type: transcript.TurnStarted, Metadata: metadata})
	_, _ = store.Append(context.Background(), transcript.Event{ID: "done", SessionID: sessionID, TurnID: "first", Type: transcript.TurnCompleted, Status: string(agentruntime.TerminationPendingInput)})
	accepted, err := service.handle(context.Background(), slackconversation.Request{EventID: "explicit", UserID: "U1", Channel: "C", ThreadTS: "T", Text: "production"})
	if err != nil || !accepted {
		t.Fatal("explicit mention did not resume pending input")
	}
}

func TestStreamProjectsRuntimeTerminalStateToSession(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", Text: "diagnose"})
	stream.Start()
	if got := messenger.statuses; len(got) != 1 || got[0] != sessionProcessing {
		t.Fatalf("start session status = %#v", got)
	}
	stream.Lifecycle(transcript.Event{Type: transcript.TurnStarted})
	stream.Lifecycle(transcript.Event{Type: transcript.ToolCallStarted, ToolCall: &tool.Call{Name: "repo-search"}})
	stream.Lifecycle(transcript.Event{Type: transcript.TurnCompleted})
	if got := messenger.statuses; len(got) != 1 {
		t.Fatalf("status cleared before final delivery: %#v", got)
	}
	stream.Complete("")
	if got := messenger.statuses; len(got) != 2 || got[1] != sessionActive {
		t.Fatalf("statuses=%#v", got)
	}
}

func TestStreamKeepsDelegatedReportsInternal(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
	message := model.TextMessage(model.RoleAssistant, "unverified worker report")
	stream.Lifecycle(transcript.Event{Type: transcript.DelegatedTaskCompleted, Message: &message})
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if len(messenger.posts) != 0 {
		t.Fatalf("delegated report was exposed: %+v", messenger.posts)
	}
}

func TestStreamSuspendsSessionWhenRuntimeNeedsUserInput(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", UserID: "U"})
	stream.Start()
	stream.Lifecycle(transcript.Event{Type: transcript.TurnCompleted, Status: string(agentruntime.TerminationPendingInput)})
	stream.Complete("")
	if got := messenger.statuses; len(got) != 2 || got[0] != sessionProcessing || got[1] != sessionSuspended {
		t.Fatalf("statuses=%#v", got)
	}
}

func TestStreamSuspendsSessionWhenRuntimeNeedsApproval(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", UserID: "U"})
	stream.Start()
	stream.Lifecycle(transcript.Event{Type: transcript.TurnCompleted, Status: string(agentruntime.TerminationPendingApproval)})
	stream.Complete("")
	if got := messenger.statuses; len(got) != 2 || got[0] != sessionProcessing || got[1] != sessionSuspended {
		t.Fatalf("statuses=%#v", got)
	}
}

func TestEnrichInputWithThreadImages(t *testing.T) {
	history := []model.Message{{Role: model.RoleUser, Content: []model.Content{
		{Type: model.ContentText, Text: "pets"},
		{Type: model.ContentImage, ImageURL: "data:image/png;base64,abc"},
	}}}
	input := model.TextMessage(model.RoleUser, "再看看").WithImages(model.CollectImages(history...))
	if len(input.Content) != 2 || input.Content[1].Type != model.ContentImage {
		t.Fatalf("input=%+v", input.Content)
	}
}

func TestUnsupportedImagesAreRemovedWithoutMutatingInput(t *testing.T) {
	input := model.Message{Role: model.RoleUser, Content: []model.Content{
		{Type: model.ContentText, Text: "看看"},
		{Type: model.ContentImage, ImageURL: "https://example.test/image.png"},
	}}
	got := withoutUnsupportedImages(input, true)
	if len(input.Content) != 2 || len(got.Content) != 2 || got.Content[0].Text != "看看" || !strings.Contains(got.Content[1].Text, "1 个图片附件") {
		t.Fatalf("input=%+v got=%+v", input.Content, got.Content)
	}
	for _, block := range got.Content {
		if block.Type == model.ContentImage {
			t.Fatalf("image was retained: %+v", got.Content)
		}
	}
}

func TestCompletePostsFinalWithoutStartingEmptyStream(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", UserID: "U"})
	stream.Complete("answer")
	if len(messenger.posts) != 1 || messenger.posts[0] != "answer" {
		t.Fatalf("posts=%#v", messenger.posts)
	}
}

type contextCheckingMessenger struct{ err error }

func (m *contextCheckingMessenger) PostMessage(ctx context.Context, _, _, _ string) (string, error) {
	m.err = ctx.Err()
	return "post", m.err
}

func (m *contextCheckingMessenger) PostMarkdownMessage(ctx context.Context, _, _, _ string) (string, error) {
	m.err = ctx.Err()
	return "post", m.err
}

func TestFailureDeliveryOutlivesCanceledRunContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	messenger := &contextCheckingMessenger{}
	stream := newSlackStream(ctx, messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
	if _, err := stream.Fail("failed", false); err != nil || messenger.err != nil {
		t.Fatalf("failure delivery inherited canceled run: err=%v context=%v", err, messenger.err)
	}
}

func TestRenderAnswerUsesStructuredCitations(t *testing.T) {
	message := model.Message{Role: model.RoleAssistant, Content: []model.Content{{
		Type: model.ContentText,
		Text: "Supported claim.",
		Citations: []model.Citation{
			{URL: "https://example.test/source", Title: "Primary source"},
			{URL: "https://example.test/source", Title: "duplicate"},
		},
	}}}
	want := "Supported claim.\n\nSources: [Primary source](https://example.test/source)"
	if got := renderAnswer(message); got != want {
		t.Fatalf("renderAnswer() = %q, want %q", got, want)
	}
}

func TestRenderAnswerPreservesLiteralContent(t *testing.T) {
	text := "```text\none\u00a0two\r\nthree\n```"
	message := model.TextMessage(model.RoleAssistant, text)
	if got := renderAnswer(message); got != text {
		t.Fatalf("renderAnswer() = %q, want literal code content preserved", got)
	}
}

func TestQueuedInputSurvivesServiceReplacement(t *testing.T) {
	inputs := &memoryInputs{}
	first := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	first.Inputs = inputs
	request := slackconversation.Request{EventID: "event-queued", UserID: "U1", Channel: "C", ThreadTS: "T", Text: "follow up"}
	if err := first.persistInput(context.Background(), request.Target().SessionID(), sessioninput.KindQueue, request); err != nil {
		t.Fatal(err)
	}

	second := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	second.Inputs = inputs
	queued, err := second.claimNextQueue(context.Background(), request.Target().SessionID())
	if err != nil || queued == nil || queued.EventID != request.EventID {
		t.Fatalf("queued=%+v err=%v", queued, err)
	}
}

func TestEventRouterKeepsCommentaryOutOfSlackMessagesAndStatus(t *testing.T) {
	messenger := &nativeStreamingMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1"})
	stream.Start()
	router := &eventRouter{streams: map[string]*slackStream{"turn": stream}}
	router.Publish(context.Background(), transcript.Event{TurnID: "turn", Type: transcript.ModelStreamed, Model: &model.StreamEvent{Type: model.StreamCommentary, Text: "Let me inspect the service."}})
	stream.mu.Lock()
	answer := stream.answer.String()
	stream.mu.Unlock()
	messenger.mu.Lock()
	if answer != "" || messenger.started != 0 || len(messenger.posts) != 0 || len(messenger.planPosts) != 0 || len(messenger.statuses) != 1 || messenger.statuses[0] != sessionProcessing {
		t.Fatalf("commentary changed Slack presentation: answer=%q statuses=%v", answer, messenger.statuses)
	}
	messenger.mu.Unlock()
	router.Publish(context.Background(), transcript.Event{TurnID: "turn", Type: transcript.ModelStreamed, Model: &model.StreamEvent{Type: model.StreamTextDelta, Text: "Final answer."}})
	stream.flushDeferredStream()
	stream.stopStreamTimer()
	stream.setSessionStatus(sessionActive)
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.answer.String() != "Final answer." {
		t.Fatalf("answer=%q", stream.answer.String())
	}
}

func TestImageCapabilityControlsSlackInputWithoutDroppingOnLookupFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		supported bool
		lookupErr error
	}{
		{name: "vision", supported: true}, {name: "text-only"}, {name: "catalog failure", lookupErr: fmt.Errorf("catalog unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog, _ := tool.NewCatalog()
			service := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
			client := &replyModel{}
			runner, err := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore(), Events: service.EventSink()})
			if err != nil {
				t.Fatal(err)
			}
			service.Agent.Runtime = runner
			service.Multimodal = func(context.Context, string) (bool, error) { return test.supported, test.lookupErr }
			_, err = service.handle(context.Background(), slackconversation.Request{EventID: "image", UserID: "U", Channel: "D", ThreadTS: "1", Text: "what is this?", Content: []model.Content{{Type: model.ContentImage, ImageURL: "data:image/png;base64,AAAA"}}})
			if test.lookupErr != nil {
				if err == nil || client.request != nil {
					t.Fatalf("catalog failure reached inference: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if client.request == nil {
				t.Fatal("no model request")
			}
			images := false
			for _, message := range client.request.Messages {
				images = images || model.ContainImages(message)
			}
			if images != test.supported {
				t.Fatalf("image preserved=%t supported=%t", images, test.supported)
			}
		})
	}
}

func TestQueuedApprovalUsesTheDequeuedActorAndDecision(t *testing.T) {
	ctx := context.Background()
	store := transcript.NewMemoryStore()
	call := tool.Call{ID: "pending", Name: "write", Scope: tool.Scope{UserID: "U1"}, Arguments: json.RawMessage(`{}`)}
	_, err := store.Append(ctx, transcript.Event{SessionID: session.ID("C1", "T1"), TurnID: "original", Type: transcript.ApprovalRequested, ToolCall: &call})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := tool.NewCatalog()
	client := &replyModel{}
	runner, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	service := New(hosted.Agent{Runtime: runner}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	service.Inputs = &memoryInputs{}
	queued := slackconversation.Request{EventID: "decision", UserID: "U2", Channel: "C1", ThreadTS: "T1", Approval: &slackconversation.ApprovalRequest{TurnID: "original", ToolCallID: "pending", Approved: false}}
	if err := service.persistInput(ctx, session.ID("C1", "T1"), sessioninput.KindQueue, queued); err != nil {
		t.Fatal(err)
	}
	claimed, err := service.claimNextQueue(ctx, session.ID("C1", "T1"))
	if err != nil || claimed == nil {
		t.Fatalf("claim queued approval: %v", err)
	}
	err = service.run(ctx, session.ID("C1", "T1"), *claimed)
	if err == nil || !strings.Contains(err.Error(), "only the requesting user") {
		t.Fatalf("queued approval was lost: %v", err)
	}
	if client.request != nil {
		t.Fatal("model ran before resolving the queued approval")
	}
}

func (s *memoryInputs) Requeue(_ context.Context, id, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed[id] != owner {
		return sessioninput.ErrClaimLost
	}
	for i := range s.items {
		if s.items[i].ID == id && s.items[i].Kind == sessioninput.KindSteering && !s.acked[id] {
			s.items[i].Kind = sessioninput.KindQueue
			delete(s.claimed, id)
			return nil
		}
	}
	return sessioninput.ErrClaimLost
}

func (s *memoryInputs) Renew(_ context.Context, id, owner string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed[id] != owner || s.acked[id] {
		return sessioninput.ErrClaimLost
	}
	return nil
}

func (s *memoryInputs) Fail(ctx context.Context, id, owner string, _ error, _ time.Duration, _ int) (bool, error) {
	return false, s.Release(ctx, id, owner)
}

// handle drives execution synchronously in runtime/presentation unit fixtures.
func (s *Service) handle(ctx context.Context, req slackconversation.Request) (bool, error) {
	if s.Messenger == nil || s.Agent.Runtime == nil {
		return false, nil
	}
	sessionID := session.ID(req.Channel, req.ThreadTS)
	controlled, controlErr := s.controlActive(ctx, sessionID, req)
	if controlErr != nil {
		return false, controlErr
	}
	if controlled {
		return true, nil
	}
	return true, s.run(ctx, sessionID, claimedInput{Request: req})
}

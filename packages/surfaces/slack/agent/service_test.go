package slackagent

import (
	"context"
	"encoding/json"
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
	"github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	"github.com/noknov/kepler-agent/packages/workflows"
	"github.com/noknov/kepler-agent/packages/workflows/codereview"
)

type replyModel struct{ request *model.Request }

type fixedIntentRouter struct {
	decision workflows.RouteDecision
	request  workflows.RouteRequest
}

func (r *fixedIntentRouter) Route(_ context.Context, request workflows.RouteRequest) (workflows.RouteDecision, error) {
	r.request = request
	return r.decision, nil
}

type deferredReviewTool struct{ name string }

func codeReviewWorkflows() workflows.Registry {
	return workflows.NewRegistry(codereview.Definition{})
}

func (t deferredReviewTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{
		Name: t.name, InputSchema: json.RawMessage(`{"type":"object"}`),
		Effects: []tool.Effect{tool.EffectRead}, Exposure: tool.ExposureDeferred,
	}
}

func (deferredReviewTool) Execute(context.Context, tool.Call) (tool.Result, error) {
	return tool.TextResult("ok"), nil
}

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
	accepted, handleErr := service.HandleMention(context.Background(), slackconversation.Request{EventID: "event-1", UserID: "U1", Channel: "C1", ThreadTS: "T1", Text: "question"})
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

func TestServiceRoutesCodeReviewIntentToDedicatedWorkflow(t *testing.T) {
	catalog, err := tool.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{}
	service := New(hosted.Agent{}, messenger, safety.PromptPolicy{}, safety.Redactor{}, nil)
	service.Workflows = codeReviewWorkflows()
	service.IntentRouter = &fixedIntentRouter{decision: workflows.RouteDecision{Intent: codereview.WorkflowName, Inputs: map[string]string{codereview.InputMode: "deep"}}}
	client := &replyModel{}
	runner, err := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore(), Events: service.EventSink()})
	if err != nil {
		t.Fatal(err)
	}
	service.Agent.Runtime = runner
	_, err = service.HandleMention(context.Background(), slackconversation.Request{
		EventID: "review-1", UserID: "U1", Channel: "C1", ThreadTS: "T1",
		Text: "Deep review https://github.com/acme/widgets/pull/42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.request == nil || len(client.request.Messages) == 0 {
		t.Fatal("model did not receive request")
	}
	system := client.request.Messages[0].Text()
	for _, want := range []string{"dedicated multi-agent pull-request review workflow", "targeted verification", "Requested review mode: deep"} {
		if !strings.Contains(system, want) {
			t.Fatalf("review system prompt missing %q", want)
		}
	}
}

func TestSlackIntentRouterActivatesCodeReviewFromUserMessage(t *testing.T) {
	catalog, err := tool.NewCatalog(
		deferredReviewTool{name: "github-pr_diff"},
		deferredReviewTool{name: "github-pr_file_diff"},
	)
	if err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{}
	service := New(hosted.Agent{}, messenger, safety.PromptPolicy{}, safety.Redactor{}, nil)
	service.Workflows = codeReviewWorkflows()
	router := &fixedIntentRouter{decision: workflows.RouteDecision{Intent: codereview.WorkflowName, Inputs: map[string]string{codereview.InputMode: "deep"}}}
	service.IntentRouter = router
	client := &replyModel{}
	runner, err := agentruntime.New(agentruntime.Config{Model: "primary"}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore(), Events: service.EventSink()})
	if err != nil {
		t.Fatal(err)
	}
	service.Agent.Runtime = runner

	accepted, err := service.HandleMention(context.Background(), slackconversation.Request{
		EventID: "review-routed", UserID: "U1", Channel: "C1", ThreadTS: "T1",
		Text: "请 deep review https://github.com/acme/widgets/pull/42",
	})
	if err != nil || !accepted {
		t.Fatalf("accepted=%v err=%v", accepted, err)
	}
	if client.request == nil || !strings.Contains(client.request.Messages[0].Text(), "Requested review mode: deep") {
		t.Fatalf("request=%+v", client.request)
	}
	if router.request.SessionID == "" {
		t.Fatal("intent router did not receive the stable Slack session ID")
	}
}

func TestCodeReviewThreadReplyContinuesDurableWorkflowWithoutMention(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	messenger := &fakeMessenger{}
	service := New(hosted.Agent{}, messenger, safety.PromptPolicy{}, safety.Redactor{}, nil)
	service.Workflows = codeReviewWorkflows()
	service.IntentRouter = &fixedIntentRouter{decision: workflows.RouteDecision{Intent: codereview.WorkflowName, Inputs: map[string]string{codereview.InputMode: "deep"}}}
	client := &replyModel{}
	runner, _ := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore(), Events: service.EventSink()})
	service.Agent.Runtime = runner

	request := slackconversation.Request{
		EventID: "review", UserID: "U1", Channel: "C1", ThreadTS: "T1", Text: "Deep review https://github.com/acme/widgets/pull/42",
	}
	if accepted, err := service.HandleMention(context.Background(), request); err != nil || !accepted {
		t.Fatalf("initial accepted=%v err=%v", accepted, err)
	}
	request.EventID = "follow-up"
	request.Text = "第二个候选问题为什么被确认？"
	if accepted, err := service.HandleReply(context.Background(), request); err != nil || !accepted {
		t.Fatalf("follow-up accepted=%v err=%v", accepted, err)
	}
	if client.request == nil || !strings.Contains(client.request.Messages[0].Text(), "continuing an existing pull-request review") {
		t.Fatalf("follow-up did not retain workflow: %+v", client.request)
	}
	if !strings.Contains(client.request.Messages[0].Text(), "https://github.com/acme/widgets/pull/42") {
		t.Fatalf("follow-up lost PR identity: %q", client.request.Messages[0].Text())
	}
}

func TestServiceActivatesKnownCodeReviewToolsWithoutToolSearch(t *testing.T) {
	catalog, err := tool.NewCatalog(
		deferredReviewTool{name: "github-pr_diff"},
		deferredReviewTool{name: "github-pr_file_diff"},
	)
	if err != nil {
		t.Fatal(err)
	}
	service := Service{Tools: catalog}
	if err := service.activateWorkflowTools("review-session", []string{"github-pr_diff", "github-pr_file_diff"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"github-pr_diff", "github-pr_file_diff"} {
		if _, ok := catalog.GetActive("review-session", name); !ok {
			t.Fatalf("%s was not activated", name)
		}
	}
}

func TestHandleReplyOnlyConsumesPendingInputFromItsOwner(t *testing.T) {
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
	accepted, err := service.HandleReply(context.Background(), slackconversation.Request{EventID: "wrong", UserID: "U2", Channel: "C", ThreadTS: "T", Text: "production"})
	if err != nil || accepted {
		t.Fatal("reply from a different user was consumed")
	}
	accepted, err = service.HandleReply(context.Background(), slackconversation.Request{EventID: "right", UserID: "U1", Channel: "C", ThreadTS: "T", Text: "production"})
	if err != nil || !accepted {
		t.Fatal("pending reply from the owner was ignored")
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
	if got := messenger.statuses; len(got) != 2 || got[0] != sessionProcessing || got[1] != sessionSuspended {
		t.Fatalf("statuses=%#v", got)
	}
}

func TestStreamSuspendsSessionWhenRuntimeNeedsApproval(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", UserID: "U"})
	stream.Start()
	stream.Lifecycle(transcript.Event{Type: transcript.TurnCompleted, Status: string(agentruntime.TerminationPendingApproval)})
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

func TestRenderAnswerNormalizesNonBreakingSpaces(t *testing.T) {
	message := model.TextMessage(model.RoleAssistant, "one\u00a0two\r\nthree")
	if got := renderAnswer(message); got != "one two\r\nthree" {
		t.Fatalf("renderAnswer() = %q, want normalized spaces", got)
	}
}

func TestQueuedInputSurvivesServiceReplacement(t *testing.T) {
	inputs := &memoryInputs{}
	first := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	first.Inputs = inputs
	request := slackconversation.Request{EventID: "event-queued", UserID: "U1", Text: "follow up"}
	if err := first.persistInput(context.Background(), "session", sessioninput.KindQueue, request); err != nil {
		t.Fatal(err)
	}

	second := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	second.Inputs = inputs
	queued, err := second.claimNextQueue(context.Background(), "session")
	if err != nil || queued == nil || queued.EventID != request.EventID {
		t.Fatalf("queued=%+v err=%v", queued, err)
	}
}

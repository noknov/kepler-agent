// Package slackagent adapts Slack requests and presentation to the shared
// hosted harness. Slack remains an ingress, not a separate agent runtime.
package slackagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/failure"
	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/slack-copilot-agent/packages/agent/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
	"github.com/noknov/slack-copilot-agent/packages/connections"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/profiles/hosted"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/session"
	"github.com/noknov/slack-copilot-agent/packages/sessioninput"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
	"github.com/redis/go-redis/v9"
)

type ConversationMode string

// ThreadLoader fetches Slack thread history for the current turn.
type ThreadLoader interface {
	Load(context.Context, slackconversation.Request) ([]model.Message, error)
}

const (
	ModeSteer ConversationMode = "steer"
	ModeQueue ConversationMode = "queue"
)

const slackOutputFormatPrompt = `This response is delivered through Slack's Markdown block. Format it as concise, conservative Slack Markdown: use readable paragraphs, simple lists, links, inline code, and code fences when needed. Normalize retrieved evidence instead of copying source-only wrappers, code-fence language labels, or unusual whitespace.`

type Service struct {
	Agent            hosted.Agent
	Messenger        slackconversation.Messenger
	Prompt           safety.PromptPolicy
	Redactor         safety.Redactor
	UserPrefs        userprefs.Store
	Workspace        string
	Redis            *redisclient.Client
	Continuations    connections.ContinuationStore
	PodID            string
	Lifecycle        context.Context
	ModeForUser      func(string) ConversationMode
	ModelFor         func(slackconversation.Request) string
	OnDelivered      func(context.Context, string, string, string) error
	AlreadyDelivered func(context.Context, string) (bool, error)
	Multimodal       func(string) bool
	MultimodalModel  func() string
	ThreadLoader     ThreadLoader
	WebSearchEnabled func(string) bool
	Progress         *ProgressSummarizer
	Locker           session.Locker
	Inputs           sessioninput.Store
	BeforeRun        func(context.Context, string) error

	mu     sync.Mutex
	active map[string]*activeRun
	router *eventRouter
}

type activeRun struct {
	userID   string
	cancel   context.CancelFunc
	steering *steeringInput
}

type steeringInput struct {
	store     sessioninput.Store
	sessionID string
	owner     string
}

func (s *steeringInput) Push(request slackconversation.Request) error {
	if s.store == nil {
		return fmt.Errorf("durable session input store is unavailable")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return s.store.Enqueue(context.Background(), sessioninput.Item{ID: request.EventID, SessionID: s.sessionID, Kind: sessioninput.KindSteering, Payload: payload})
}

func (s *steeringInput) Claim(ctx context.Context, limit int) ([]agentruntime.PendingInput, error) {
	if s.store == nil {
		return nil, nil
	}
	items, err := s.store.Claim(ctx, s.sessionID, sessioninput.KindSteering, s.owner, 35*time.Minute, limit)
	if err != nil {
		return nil, err
	}
	inputs := make([]agentruntime.PendingInput, 0, len(items))
	for _, item := range items {
		var request slackconversation.Request
		if err := json.Unmarshal(item.Payload, &request); err != nil {
			return nil, err
		}
		inputs = append(inputs, agentruntime.PendingInput{ID: item.ID, Message: request.Message()})
	}
	return inputs, nil
}

func (s *steeringInput) Ack(ctx context.Context, id string) error {
	if s.store == nil {
		return fmt.Errorf("durable session input store is unavailable")
	}
	return s.store.Ack(ctx, id, s.owner)
}

type eventRouter struct {
	mu      sync.RWMutex
	streams map[string]*slackStream
}

func New(agent hosted.Agent, messenger slackconversation.Messenger, policy safety.PromptPolicy, redactor safety.Redactor, prefs userprefs.Store) *Service {
	router := &eventRouter{streams: make(map[string]*slackStream)}
	return &Service{Agent: agent, Messenger: messenger, Prompt: policy, Redactor: redactor, UserPrefs: prefs, active: make(map[string]*activeRun), router: router}
}

func (s *Service) EventSink() transcript.Sink { return s.router }

func (r *eventRouter) Publish(_ context.Context, event transcript.Event) {
	r.mu.RLock()
	stream := r.streams[event.TurnID]
	r.mu.RUnlock()
	if stream == nil {
		return
	}
	stream.Lifecycle(event)
	if event.Type == transcript.ModelStreamed && event.Model != nil {
		switch event.Model.Type {
		case model.StreamTextDelta:
			stream.AppendDelta(event.Model.Text)
		case model.StreamToolCallDone:
			if event.Model.ToolCall != nil {
				stream.ToolStep([]model.ToolCall{*event.Model.ToolCall})
			}
		}
	}
	if event.Type == transcript.AssistantMessage && event.Message != nil {
		stream.CommitStep(*event.Message)
	}
}

func (r *eventRouter) set(turnID string, stream *slackStream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stream == nil {
		delete(r.streams, turnID)
	} else {
		r.streams[turnID] = stream
	}
}

func (s *Service) HandleMention(ctx context.Context, req slackconversation.Request) (bool, error) {
	return s.handle(ctx, req)
}
func (s *Service) HandleReply(ctx context.Context, req slackconversation.Request) (bool, error) {
	if s.Messenger == nil || s.Agent.Runtime == nil {
		return false, nil
	}
	sessionID := session.ID(req.Channel, req.ThreadTS)
	controlled, controlErr := s.controlActive(sessionID, req)
	if controlErr != nil {
		return false, controlErr
	}
	if controlled {
		return true, nil
	}
	waiting, err := s.Agent.Runtime.WaitingForInput(ctx, sessionID, req.UserID)
	if err != nil || !waiting {
		return false, err
	}
	return true, s.run(ctx, sessionID, req)
}

func (s *Service) handle(ctx context.Context, req slackconversation.Request) (bool, error) {
	if s.Messenger == nil || s.Agent.Runtime == nil {
		return false, nil
	}
	sessionID := session.ID(req.Channel, req.ThreadTS)
	controlled, controlErr := s.controlActive(sessionID, req)
	if controlErr != nil {
		return false, controlErr
	}
	if controlled {
		return true, nil
	}
	return true, s.run(ctx, sessionID, req)
}

func (s *Service) run(eventCtx context.Context, sessionID string, req slackconversation.Request) (runErr error) {
	base := eventCtx
	if base == nil {
		base = s.Lifecycle
	}
	if base == nil {
		base = context.Background()
	}
	runCtx, cancel := context.WithTimeout(base, 30*time.Minute)
	var unlock func()
	if s.Locker != nil {
		var err error
		unlock, err = s.Locker.Lock(runCtx, "session:"+sessionID)
		if err != nil {
			cancel()
			log.Printf("slack conversation lock failed session=%s: %v", sessionID, err)
			_, deliveryErr := s.Messenger.PostMessage(base, req.Channel, req.ThreadTS, failure.PublicMessage(err))
			if deliveryErr != nil {
				return errors.Join(err, deliveryErr)
			}
			return nil
		}
		defer unlock()
	}
	if req.ClaimID == "" {
		queued, err := s.claimNextQueue(runCtx, sessionID)
		if err != nil {
			cancel()
			return err
		}
		if queued != nil {
			if err := s.persistInput(runCtx, sessionID, sessioninput.KindQueue, req); err != nil {
				cancel()
				_ = s.releaseClaim(context.Background(), queued.ClaimID)
				return err
			}
			req = *queued
		}
	}
	owner := s.PodID
	if owner == "" {
		owner = "local-slack-service"
	}
	active := &activeRun{userID: req.UserID, cancel: cancel, steering: &steeringInput{store: s.Inputs, sessionID: sessionID, owner: owner}}
	if existing := s.register(sessionID, active); existing != nil {
		cancel()
		if existing.userID != req.UserID {
			return fmt.Errorf("conversation session is already owned by another user")
		}
		if s.mode(req.UserID) == ModeQueue {
			if err := s.persistInput(base, sessionID, sessioninput.KindQueue, req); err != nil {
				return err
			}
		} else {
			if err := existing.steering.Push(req); err != nil {
				return err
			}
		}
		return nil
	}
	defer func() {
		cancel()
		s.unregister(sessionID, active)
		if s.Inputs != nil {
			promoteCtx, promoteCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, promoteErr := s.Inputs.PromoteSteering(promoteCtx, sessionID)
			promoteCancel()
			if runErr == nil && promoteErr != nil {
				runErr = promoteErr
			}
		}
		if runErr != nil && req.ClaimID != "" {
			_ = s.releaseClaim(context.Background(), req.ClaimID)
		}
		if runErr == nil {
			followCtx := s.Lifecycle
			if followCtx == nil {
				followCtx = context.Background()
			}
			go s.startPending(followCtx, sessionID)
		}
	}()

	if s.BeforeRun != nil {
		if err := s.BeforeRun(runCtx, req.UserID); err != nil {
			cancel()
			return err
		}
	}

	turnID := strings.TrimSpace(req.EventID)
	if turnID == "" {
		turnID = fmt.Sprintf("slack-%d", time.Now().UnixNano())
	}
	// The same stable identifier drives transcript replay and Slack's
	// client_msg_id. Keep generated IDs on the request as well as the turn.
	req.EventID = turnID
	stream := newSlackStream(runCtx, s.Messenger, req)
	stream.redactor = safety.NewStreamRedactor(s.Redactor)
	stream.progress = s.Progress
	s.router.set(turnID, stream)
	defer s.router.set(turnID, nil)
	stream.Start()

	fragments := []prompt.Fragment{
		{ID: "hosted-core", Version: "1", Layer: prompt.LayerCore, Content: s.Prompt.SystemPrompt()},
		{ID: "slack-output-format", Version: "1", Layer: prompt.LayerProduct, Content: slackOutputFormatPrompt},
		{ID: "user-rules", Layer: prompt.LayerUser, Content: userprefs.RulesPrompt(runCtx, s.UserPrefs, req.UserID)},
		{ID: "user-skills", Layer: prompt.LayerSkill, Content: userprefs.SkillsMetadataPrompt(runCtx, s.UserPrefs, req.UserID)},
	}
	var history []model.Message
	if s.ThreadLoader != nil {
		loaded, loadErr := s.ThreadLoader.Load(runCtx, req)
		if loadErr != nil {
			return fmt.Errorf("load Slack thread history: %w", loadErr)
		}
		history = loaded
	}
	modelName := ""
	if s.ModelFor != nil {
		modelName = s.ModelFor(req)
	}
	threadImages := model.CollectImages(history...)
	input := req.Message().WithImages(threadImages)
	if len(threadImages) > 0 {
		log.Printf("slack thread context: %d history messages, %d images attached to turn input", len(history), len(threadImages))
	}
	if s.Multimodal != nil && !s.Multimodal(modelName) && model.ContainImages(append([]model.Message{input}, history...)...) {
		if s.MultimodalModel != nil {
			if fallback := strings.TrimSpace(s.MultimodalModel()); fallback != "" {
				modelName = fallback
			}
		}
	}
	if s.Multimodal != nil && !s.Multimodal(modelName) {
		input = withoutUnsupportedImages(input, slackconversation.IsChineseLocale(req.Locale))
		history = stripUnsupportedImages(history, slackconversation.IsChineseLocale(req.Locale))
	}
	webSearch := "enabled"
	if s.WebSearchEnabled != nil && !s.WebSearchEnabled(req.UserID) {
		webSearch = "disabled"
	}
	result, err := s.Agent.Run(runCtx, hosted.Request{SessionID: sessionID, TurnID: turnID, UserID: req.UserID, Workspace: s.Workspace, Input: input, History: history, Model: modelName, Steering: active.steering, Prompt: fragments, ScopeValues: map[string]string{"channel": req.Channel, "thread_ts": req.ThreadTS, "message_ts": req.MessageTS, "web_search": webSearch}})
	finalizeCtx, finalizeCancel := context.WithTimeout(context.WithoutCancel(runCtx), 20*time.Second)
	defer finalizeCancel()
	if err != nil {
		if s.AlreadyDelivered != nil {
			delivered, deliveryStateErr := s.AlreadyDelivered(finalizeCtx, turnID)
			if deliveryStateErr != nil {
				return deliveryStateErr
			}
			if delivered {
				stream.clearStatus()
				return s.ackClaim(finalizeCtx, req.ClaimID)
			}
		}
		log.Printf("slack agent run failed session=%s turn=%s: %v", sessionID, turnID, err)
		messageTS, deliveryErr := stream.Fail(failure.PublicMessage(err), errors.Is(err, context.Canceled))
		if deliveryErr != nil {
			return deliveryErr
		}
		if s.OnDelivered != nil && messageTS != "" {
			if linkErr := s.OnDelivered(finalizeCtx, turnID, req.Channel, messageTS); linkErr != nil {
				return linkErr
			}
		}
		return s.ackClaim(finalizeCtx, req.ClaimID)
	}
	final := s.Redactor.Sanitize(renderAnswer(result.Message))
	if s.AlreadyDelivered != nil {
		delivered, deliveryStateErr := s.AlreadyDelivered(finalizeCtx, turnID)
		if deliveryStateErr != nil {
			return deliveryStateErr
		}
		if delivered {
			stream.clearStatus()
			return s.ackClaim(finalizeCtx, req.ClaimID)
		}
	}
	messageTS, err := stream.Complete(final)
	if err != nil {
		return err
	}
	if s.OnDelivered != nil && messageTS != "" {
		if err := s.OnDelivered(finalizeCtx, turnID, req.Channel, messageTS); err != nil {
			return err
		}
	}
	return s.ackClaim(finalizeCtx, req.ClaimID)
}

func renderAnswer(message model.Message) string {
	answer := strings.TrimSpace(strings.ReplaceAll(message.Text(), "\u00a0", " "))
	seen := make(map[string]bool)
	var sources []string
	for _, citation := range message.Citations() {
		url := strings.TrimSpace(citation.URL)
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		title := strings.TrimSpace(citation.Title)
		if title == "" {
			title = url
		}
		sources = append(sources, "["+title+"]("+url+")")
	}
	if len(sources) == 0 {
		return answer
	}
	return answer + "\n\nSources: " + strings.Join(sources, ", ")
}

func withoutUnsupportedImages(message model.Message, cjk bool) model.Message {
	content := make([]model.Content, 0, len(message.Content)+1)
	removed := 0
	for _, block := range message.Content {
		if block.Type == model.ContentImage {
			removed++
			continue
		}
		content = append(content, block)
	}
	if removed == 0 {
		return message
	}
	note := fmt.Sprintf("[%d image attachment(s) omitted because the selected model does not support images.]", removed)
	if cjk {
		note = fmt.Sprintf("[已省略 %d 个图片附件：当前选择的模型不支持图片。]", removed)
	}
	content = append(content, model.Content{Type: model.ContentText, Text: note})
	message.Content = content
	return message
}

func stripUnsupportedImages(messages []model.Message, cjk bool) []model.Message {
	if len(messages) == 0 {
		return messages
	}
	out := make([]model.Message, len(messages))
	for i, message := range messages {
		out[i] = withoutUnsupportedImages(message, cjk)
	}
	return out
}

func (s *Service) mode(userID string) ConversationMode {
	if s.ModeForUser != nil {
		if mode := s.ModeForUser(userID); mode == ModeQueue {
			return mode
		}
	}
	return ModeSteer
}

func (s *Service) controlActive(sessionID string, req slackconversation.Request) (bool, error) {
	s.mu.Lock()
	active := s.active[sessionID]
	s.mu.Unlock()
	if active != nil && active.userID == req.UserID {
		if s.mode(req.UserID) == ModeQueue {
			if err := s.persistInput(context.Background(), sessionID, sessioninput.KindQueue, req); err != nil {
				return false, err
			}
		} else {
			if err := active.steering.Push(req); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	if s.Redis == nil || strings.TrimSpace(req.ThreadTS) == "" {
		return false, nil
	}
	owner, err := s.Redis.Get(context.Background(), "agent:active:"+sessionID)
	if err != nil || owner == "" || owner == s.PodID {
		// Redis is only a routing hint. If it is unavailable or the hint has
		// expired, the durable inbox and PG session lock still serialize this
		// request as a later turn.
		return false, nil
	}
	kind := sessioninput.KindSteering
	if s.mode(req.UserID) == ModeQueue {
		kind = sessioninput.KindQueue
	}
	if err := s.persistInput(context.Background(), sessionID, kind, req); err != nil {
		return false, err
	}
	// Pub/Sub is only a latency hint. The request is accepted because it was
	// durably enqueued, never because a subscriber happened to be present.
	_, _ = s.Redis.PublishCount(context.Background(), "agent:control:"+owner, sessionID)
	return true, nil
}

func (s *Service) persistInput(ctx context.Context, sessionID string, kind sessioninput.Kind, request slackconversation.Request) error {
	if s.Inputs == nil {
		return fmt.Errorf("durable session input store is unavailable")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return s.Inputs.Enqueue(ctx, sessioninput.Item{ID: request.EventID, SessionID: sessionID, Kind: kind, Payload: payload})
}

func (s *Service) claimNextQueue(ctx context.Context, sessionID string) (*slackconversation.Request, error) {
	if s.Inputs == nil {
		return nil, nil
	}
	owner := s.PodID
	if owner == "" {
		owner = "local-slack-service"
	}
	items, err := s.Inputs.Claim(ctx, sessionID, sessioninput.KindQueue, owner, 35*time.Minute, 1)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	var request slackconversation.Request
	if err := json.Unmarshal(items[0].Payload, &request); err != nil {
		_ = s.Inputs.Release(context.Background(), items[0].ID, owner)
		return nil, err
	}
	request.ClaimID = items[0].ID
	return &request, nil
}

func (s *Service) ackClaim(ctx context.Context, id string) error {
	if id == "" || s.Inputs == nil {
		return nil
	}
	owner := s.PodID
	if owner == "" {
		owner = "local-slack-service"
	}
	return s.Inputs.Ack(ctx, id, owner)
}

func (s *Service) releaseClaim(ctx context.Context, id string) error {
	if id == "" || s.Inputs == nil {
		return nil
	}
	owner := s.PodID
	if owner == "" {
		owner = "local-slack-service"
	}
	return s.Inputs.Release(ctx, id, owner)
}

func (s *Service) startPending(ctx context.Context, sessionID string) {
	s.mu.Lock()
	active := s.active[sessionID]
	s.mu.Unlock()
	if active != nil {
		return
	}
	request, err := s.claimNextQueue(ctx, sessionID)
	if err != nil || request == nil {
		return
	}
	_ = s.run(ctx, sessionID, *request)
}

func (s *Service) register(sessionID string, run *activeRun) *activeRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.active[sessionID]; existing != nil {
		return existing
	}
	s.active[sessionID] = run
	if s.Redis != nil {
		_ = s.Redis.Set(context.Background(), "agent:active:"+sessionID, s.PodID, 35*time.Minute)
	}
	return nil
}

func (s *Service) unregister(sessionID string, run *activeRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[sessionID] == run {
		delete(s.active, sessionID)
	}
	if s.Redis != nil {
		_ = s.Redis.Del(context.Background(), "agent:active:"+sessionID)
	}
}

func (s *Service) StartConnectionCompletedSubscriber(ctx context.Context) {
	if s.Redis == nil || s.Continuations == nil || s.Agent.Runtime == nil {
		return
	}
	sub := s.Redis.Subscribe(ctx, connections.OAuthCompletedChannel)
	defer func() { _ = sub.Close() }()
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-sub.Channel():
			if !ok {
				return
			}
			userID, provider, parsed := connections.ParseOAuthCompletedPayload(message.Payload)
			if !parsed {
				continue
			}
			continuations, err := s.Continuations.Claim(ctx, userID, provider)
			if err != nil {
				log.Printf("connection continuation claim failed user=%s provider=%s: %v", userID, provider, err)
				continue
			}
			for _, continuation := range continuations {
				go s.resumeAfterConnection(ctx, continuation)
			}
		}
	}
}

func (s *Service) resumeAfterConnection(parentCtx context.Context, continuation connections.Continuation) {
	if continuation.Channel == "" || continuation.UserID == "" {
		if s.Continuations != nil {
			_ = s.Continuations.Clear(context.Background(), continuation)
		}
		return
	}
	base := s.Lifecycle
	if base == nil {
		base = parentCtx
	}
	if base == nil {
		base = context.Background()
	}
	ctx := context.WithoutCancel(base)

	sessionID := session.ID(continuation.Channel, continuation.ThreadTS)
	waiting, err := s.Agent.Runtime.WaitingForInput(ctx, sessionID, continuation.UserID)
	if err != nil {
		log.Printf("connection continuation waiting check failed session=%s: %v", sessionID, err)
		s.releaseContinuation(ctx, continuation)
		return
	}
	if !waiting {
		// The turn was completed or superseded while OAuth was in progress.
		_ = s.Continuations.Clear(ctx, continuation)
		return
	}
	req := slackconversation.Request{
		EventID:  fmt.Sprintf("connection-continue-%s-%d", continuation.Provider, time.Now().UnixNano()),
		UserID:   continuation.UserID,
		Channel:  continuation.Channel,
		ThreadTS: continuation.ThreadTS,
		Text:     "I've connected. Please continue.",
	}
	if s.Messenger != nil {
		if _, err := s.Messenger.PostMessage(ctx, continuation.Channel, continuation.ThreadTS, "Connected — continuing…"); err != nil {
			log.Printf("connection continuation status post failed session=%s: %v", sessionID, err)
		}
	}
	accepted, err := s.HandleReply(ctx, req)
	if err != nil {
		log.Printf("connection continuation resume failed session=%s: %v", sessionID, err)
		s.releaseContinuation(ctx, continuation)
		return
	}
	if !accepted {
		log.Printf("connection continuation resume not accepted session=%s", sessionID)
		s.releaseContinuation(ctx, continuation)
		return
	}
	if s.Continuations != nil {
		_ = s.Continuations.Clear(ctx, continuation)
	}
}

func (s *Service) releaseContinuation(ctx context.Context, continuation connections.Continuation) {
	if s.Continuations != nil {
		_ = s.Continuations.Release(ctx, continuation)
	}
}

func (s *Service) StartControlSubscriber(ctx context.Context) {
	if (s.Redis == nil || s.PodID == "") && s.Inputs == nil {
		return
	}
	var messages <-chan *redis.Message
	var closeSub func()
	if s.Redis != nil && s.PodID != "" {
		sub := s.Redis.Subscribe(ctx, "agent:control:"+s.PodID)
		messages = sub.Channel()
		closeSub = func() { _ = sub.Close() }
		defer closeSub()
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-messages:
			if !ok {
				messages = nil
				continue
			}
			s.startPending(ctx, message.Payload)
		case <-ticker.C:
			if s.Inputs == nil {
				continue
			}
			_, _ = s.Inputs.PromoteExpiredSteering(ctx, 36*time.Minute)
			sessions, err := s.Inputs.PendingSessions(ctx, sessioninput.KindQueue, 100)
			if err != nil {
				continue
			}
			for _, sessionID := range sessions {
				go s.startPending(ctx, sessionID)
			}
		}
	}
}

type slackStream struct {
	ctx                  context.Context
	messenger            slackconversation.Messenger
	req                  slackconversation.Request
	mu                   sync.Mutex
	deliveryMu           sync.Mutex
	statusMu             sync.Mutex
	status               slackconversation.ThreadStatusMessenger
	lastStatus           string
	statusEpoch          uint64
	progress             *ProgressSummarizer
	progressSeen         map[string]bool
	redactor             *safety.StreamRedactor
	answer               strings.Builder
	messageTS            string
	nativeStream         bool
	streamDeliveryFailed bool
	streamClosed         bool
	lastStreamText       string
	lastStreamUpdate     time.Time
	streamTimer          *time.Timer
}

func newSlackStream(ctx context.Context, messenger slackconversation.Messenger, req slackconversation.Request) *slackStream {
	return &slackStream{ctx: ctx, messenger: messenger, req: req}
}
func (s *slackStream) Start() {
	if status, ok := s.messenger.(slackconversation.ThreadStatusMessenger); ok {
		s.status = status
	}
}
func (s *slackStream) CommitStep(message model.Message) {
	if calls := message.ToolCalls(); len(calls) > 0 {
		s.ToolStep(calls)
	}
}
func (s *slackStream) Complete(final string) (string, error) {
	s.stopStreamTimer()
	if s.redactor != nil {
		s.appendSanitizedDelta(s.redactor.Flush())
	}
	s.flushDeferredStream(true)
	s.mu.Lock()
	s.streamClosed = true
	messageTS := s.messageTS
	nativeStream := s.nativeStream
	deliveryFailed := s.streamDeliveryFailed
	streamed := strings.TrimSpace(s.answer.String())
	s.mu.Unlock()
	s.clearStatus()
	ctx, cancel := s.deliveryContext()
	defer cancel()
	if final == "" {
		if nativeStream && messageTS != "" {
			s.stopNativeStream(ctx)
		}
		return messageTS, nil
	}
	if nativeStream && messageTS != "" {
		trimmedFinal := strings.TrimSpace(final)
		if suffix := streamSuffix(streamed, trimmedFinal); suffix != "" && trimmedFinal != s.streamedText() {
			if err := s.appendNativeChunks(suffix); err != nil {
				deliveryFailed = true
			}
		}
		s.stopNativeStream(ctx)
		if deliveryFailed {
			if updater, ok := s.messenger.(slackconversation.MarkdownMessageUpdater); ok {
				if err := updater.UpdateMarkdownMessage(ctx, s.req.Channel, messageTS, final); err != nil {
					return messageTS, err
				}
				return messageTS, nil
			}
			// A third-party Messenger without update support cannot safely amend
			// a partial native stream. Posting is its only available fallback.
			return s.postFinalMarkdown(ctx, final)
		}
		return messageTS, nil
	}
	return s.postFinalMarkdown(ctx, final)
}

func (s *slackStream) appendSanitizedDelta(delta string) {
	if delta == "" {
		return
	}
	s.mu.Lock()
	s.answer.WriteString(delta)
	s.mu.Unlock()
}
func (s *slackStream) Fail(message string, canceled bool) (string, error) {
	s.stopStreamTimer()
	s.flushDeferredStream(true)
	s.mu.Lock()
	s.streamClosed = true
	messageTS := s.messageTS
	nativeStream := s.nativeStream
	s.mu.Unlock()
	if canceled {
		message = "Cancelled this request."
		if slackconversation.IsChineseLocale(s.req.Locale) {
			message = "已中止本次请求。"
		}
	}
	s.clearStatus()
	if message == "" {
		ctx, cancel := s.deliveryContext()
		defer cancel()
		if nativeStream && messageTS != "" {
			s.stopNativeStream(ctx)
		}
		return messageTS, nil
	}
	ctx, cancel := s.deliveryContext()
	defer cancel()
	if nativeStream && messageTS != "" {
		s.stopNativeStream(ctx)
		if updater, ok := s.messenger.(slackconversation.MarkdownMessageUpdater); ok {
			if err := updater.UpdateMarkdownMessage(ctx, s.req.Channel, messageTS, message); err != nil {
				return messageTS, err
			}
			return messageTS, nil
		}
	}
	if messenger, ok := s.messenger.(slackconversation.IdempotentMarkdownMessenger); ok {
		return messenger.PostMarkdownMessageWithID(ctx, s.req.Channel, s.req.ThreadTS, message, s.req.EventID)
	}
	return s.messenger.PostMessage(ctx, s.req.Channel, s.req.ThreadTS, message)
}

func (s *slackStream) postFinalMarkdown(ctx context.Context, final string) (string, error) {
	if messenger, ok := s.messenger.(slackconversation.IdempotentMarkdownMessenger); ok {
		return messenger.PostMarkdownMessageWithID(ctx, s.req.Channel, s.req.ThreadTS, final, s.req.EventID)
	}
	return s.messenger.PostMarkdownMessage(ctx, s.req.Channel, s.req.ThreadTS, final)
}

func (s *slackStream) clearStatus() {
	if s.status != nil {
		s.statusMu.Lock()
		defer s.statusMu.Unlock()
		s.mu.Lock()
		s.statusEpoch++
		if s.lastStatus == "" {
			s.lastStatus = "\x00"
			s.mu.Unlock()
			return
		}
		if s.lastStatus == "\x00" {
			s.mu.Unlock()
			return
		}
		s.lastStatus = "\x00"
		s.mu.Unlock()
		ctx, cancel := s.deliveryContext()
		defer cancel()
		_ = s.status.SetThreadStatus(ctx, s.req.Channel, s.req.ThreadTS, "", nil)
	}
}

func (s *slackStream) deliveryContext() (context.Context, context.CancelFunc) {
	base := s.ctx
	if base == nil {
		base = context.Background()
	}
	// Delivery must remain possible after a canceled or timed-out model run,
	// while still having a strict network deadline.
	return context.WithTimeout(context.WithoutCancel(base), 20*time.Second)
}

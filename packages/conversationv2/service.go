// Package conversationv2 adapts Slack requests and presentation to the shared
// hosted v2 harness. Slack remains an ingress, not a separate agent runtime.
package conversationv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/hosted"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/prompt"
	v2runtime "github.com/noknov/slack-copilot-agent/packages/agentv2/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/transcript"
	"github.com/noknov/slack-copilot-agent/packages/conversation"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/session"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

type ConversationMode string

const (
	ModeSteer ConversationMode = "steer"
	ModeQueue ConversationMode = "queue"
)

type Service struct {
	Agent       hosted.Agent
	Messenger   conversation.Messenger
	Prompt      safety.PromptPolicy
	Redactor    safety.Redactor
	UserPrefs   userprefs.Store
	Format      conversation.TextFormatter
	Workspace   string
	Redis       *redisclient.Client
	PodID       string
	Lifecycle   context.Context
	ModeForUser func(string) ConversationMode
	Locker      session.Locker

	mu     sync.Mutex
	active map[string]*activeRun
	router *eventRouter
}

type activeRun struct {
	userID   string
	cancel   context.CancelFunc
	steering *v2runtime.InputBuffer
	mu       sync.Mutex
	queued   []conversation.Request
}

type eventRouter struct {
	mu      sync.RWMutex
	streams map[string]*slackStream
}

func New(agent hosted.Agent, messenger conversation.Messenger, policy safety.PromptPolicy, redactor safety.Redactor, prefs userprefs.Store) *Service {
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
	if event.Type == transcript.ModelStreamed && event.Model != nil && event.Model.Type == model.StreamTextDelta && event.Model.Text != "" {
		stream.Write(event.Model.Text)
	}
	if event.Type == transcript.AssistantMessage && event.Message != nil {
		stream.CommitStep(len(event.Message.ToolCalls()) > 0)
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

func (s *Service) HandleMention(ctx context.Context, req conversation.Request) bool {
	return s.handle(ctx, req)
}
func (s *Service) HandleReply(ctx context.Context, req conversation.Request) bool {
	return s.handle(ctx, req)
}

func (s *Service) handle(ctx context.Context, req conversation.Request) bool {
	if s.Messenger == nil || s.Agent.Runtime == nil {
		return false
	}
	sessionID := session.ID(req.Channel, req.ThreadTS)
	if s.controlActive(sessionID, req) {
		return true
	}
	s.run(ctx, sessionID, req)
	return true
}

func (s *Service) run(eventCtx context.Context, sessionID string, req conversation.Request) {
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
		unlock, err = s.Locker.Lock(runCtx, "v2:"+sessionID)
		if err != nil {
			cancel()
			_, _ = s.Messenger.PostMessage(context.Background(), req.Channel, req.ThreadTS, "Unable to lock this conversation: "+s.Redactor.Sanitize(err.Error()))
			return
		}
		defer unlock()
	}
	active := &activeRun{userID: req.UserID, cancel: cancel, steering: &v2runtime.InputBuffer{}}
	if existing := s.register(sessionID, active); existing != nil {
		cancel()
		if isCancel(req.Text) {
			existing.cancel()
		} else if s.mode(req.UserID) == ModeQueue {
			existing.enqueue(req)
		} else {
			existing.steering.Push(model.TextMessage(model.RoleUser, steeringText(req)))
		}
		return
	}
	defer func() {
		cancel()
		s.unregister(sessionID, active)
		if queued := active.drainQueue(); len(queued) > 0 {
			follow := combine(queued)
			followCtx := s.Lifecycle
			if followCtx == nil {
				followCtx = context.Background()
			}
			go s.run(followCtx, sessionID, follow)
		}
	}()

	turnID := strings.TrimSpace(req.EventID)
	if turnID == "" {
		turnID = fmt.Sprintf("slack-%d", time.Now().UnixNano())
	}
	stream := newSlackStream(runCtx, s.Messenger, req)
	s.router.set(turnID, stream)
	defer s.router.set(turnID, nil)
	stream.Start()

	threadContext := s.Messenger.ThreadContext(runCtx, req.Channel, req.ThreadTS, 0)
	fragments := []prompt.Fragment{
		{ID: "hosted-core", Version: "v2", Layer: prompt.LayerCore, Content: s.Prompt.SystemPrompt()},
		{ID: "user-rules", Layer: prompt.LayerUser, Content: userprefs.RulesPrompt(runCtx, s.UserPrefs, req.UserID)},
		{ID: "user-skills", Layer: prompt.LayerSkill, Content: userprefs.SkillsMetadataPrompt(runCtx, s.UserPrefs, req.UserID)},
	}
	if strings.TrimSpace(threadContext) != "" {
		fragments = append(fragments, prompt.Fragment{ID: "slack-thread", Layer: prompt.LayerEnvironment, Content: "Slack thread context supplied by the transport:\n" + threadContext})
	}
	result, err := s.Agent.Run(runCtx, hosted.Request{SessionID: sessionID, TurnID: turnID, UserID: req.UserID, Workspace: s.Workspace, Text: req.Text, Steering: active.steering, Prompt: fragments, ScopeValues: map[string]string{"channel": req.Channel, "thread_ts": req.ThreadTS}})
	if err != nil {
		stream.Fail(s.Redactor.Sanitize(err.Error()), errors.Is(err, context.Canceled))
		return
	}
	final := s.Redactor.Sanitize(strings.TrimSpace(result.Message.Text()))
	if s.Format != nil {
		final = s.Format(final)
	}
	stream.Complete(final)
}

func (s *Service) mode(userID string) ConversationMode {
	if s.ModeForUser != nil {
		if mode := s.ModeForUser(userID); mode == ModeQueue {
			return mode
		}
	}
	return ModeSteer
}

func (s *Service) controlActive(sessionID string, req conversation.Request) bool {
	s.mu.Lock()
	active := s.active[sessionID]
	s.mu.Unlock()
	if active != nil && active.userID == req.UserID {
		if isCancel(req.Text) {
			active.cancel()
			return true
		}
		if s.mode(req.UserID) == ModeQueue {
			active.enqueue(req)
		} else {
			active.steering.Push(model.TextMessage(model.RoleUser, steeringText(req)))
		}
		return true
	}
	if s.Redis == nil || strings.TrimSpace(req.ThreadTS) == "" {
		return false
	}
	owner, err := s.Redis.Get(context.Background(), "active:v2:"+sessionID)
	if err != nil || owner == "" || owner == s.PodID {
		return false
	}
	action := string(s.mode(req.UserID))
	if isCancel(req.Text) {
		action = "cancel"
	}
	payload, _ := json.Marshal(control{Session: sessionID, Action: action, Request: req})
	count, err := s.Redis.PublishCount(context.Background(), "pod:control:v2:"+owner, string(payload))
	return err == nil && count > 0
}

func (s *Service) register(sessionID string, run *activeRun) *activeRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.active[sessionID]; existing != nil {
		return existing
	}
	s.active[sessionID] = run
	if s.Redis != nil {
		_ = s.Redis.Set(context.Background(), "active:v2:"+sessionID, s.PodID, 35*time.Minute)
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
		_ = s.Redis.Del(context.Background(), "active:v2:"+sessionID)
	}
}

type control struct {
	Session string               `json:"session"`
	Action  string               `json:"action"`
	Request conversation.Request `json:"request"`
}

func (s *Service) StartControlSubscriber(ctx context.Context) {
	if s.Redis == nil || s.PodID == "" {
		return
	}
	sub := s.Redis.Subscribe(ctx, "pod:control:v2:"+s.PodID)
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-sub.Channel():
			if !ok {
				return
			}
			var cmd control
			if json.Unmarshal([]byte(message.Payload), &cmd) != nil {
				continue
			}
			s.mu.Lock()
			active := s.active[cmd.Session]
			s.mu.Unlock()
			if active == nil || active.userID != cmd.Request.UserID {
				continue
			}
			switch cmd.Action {
			case "cancel":
				active.cancel()
			case string(ModeQueue):
				active.enqueue(cmd.Request)
			default:
				active.steering.Push(model.TextMessage(model.RoleUser, steeringText(cmd.Request)))
			}
		}
	}
}

func (a *activeRun) enqueue(req conversation.Request) {
	a.mu.Lock()
	a.queued = append(a.queued, req)
	a.mu.Unlock()
}
func (a *activeRun) drainQueue() []conversation.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := append([]conversation.Request(nil), a.queued...)
	a.queued = nil
	return out
}

func combine(requests []conversation.Request) conversation.Request {
	out := requests[0]
	var lines []string
	for _, req := range requests {
		if text := strings.TrimSpace(req.Text); text != "" {
			lines = append(lines, "- <@"+req.UserID+">: "+text)
		}
	}
	out.EventID += fmt.Sprintf(":queued:%d", time.Now().UnixNano())
	out.Text = "Queued follow-up messages:\n" + strings.Join(lines, "\n")
	return out
}

func steeringText(req conversation.Request) string {
	return "New guidance from <@" + req.UserID + "> while this turn is running:\n" + strings.TrimSpace(req.Text)
}
func isCancel(text string) bool {
	text = strings.Trim(strings.ToLower(strings.TrimSpace(text)), " .!！。")
	switch text {
	case "cancel", "stop", "abort", "interrupt", "中止", "停止", "取消":
		return true
	}
	return false
}

type slackStream struct {
	ctx       context.Context
	messenger conversation.Messenger
	req       conversation.Request
	mu        sync.Mutex
	ts        string
	pending   strings.Builder
	streamed  bool
	status    conversation.ThreadStatusMessenger
}

func newSlackStream(ctx context.Context, messenger conversation.Messenger, req conversation.Request) *slackStream {
	return &slackStream{ctx: ctx, messenger: messenger, req: req}
}
func (s *slackStream) Start() {
	status, ok := s.messenger.(conversation.ThreadStatusMessenger)
	if !ok {
		return
	}
	s.status = status
	statusText, loading := "is thinking", "Thinking..."
	if agent.DetectLocale(s.req.Text) == agent.LocaleZH {
		statusText, loading = "正在思考", "思考中..."
	}
	_ = status.SetThreadStatus(s.ctx, s.req.Channel, s.req.ThreadTS, statusText, []string{loading})
}
func (s *slackStream) Write(text string) {
	s.mu.Lock()
	s.pending.WriteString(text)
	s.mu.Unlock()
}
func (s *slackStream) CommitStep(hasToolCalls bool) {
	s.mu.Lock()
	text := s.pending.String()
	s.pending.Reset()
	ts := s.ts
	s.mu.Unlock()
	if hasToolCalls || text == "" {
		return
	}
	if ts == "" {
		var err error
		ts, err = s.messenger.StartStream(s.ctx, s.req.Channel, s.req.ThreadTS, s.req.UserID)
		if err != nil || ts == "" {
			return
		}
		s.mu.Lock()
		s.ts = ts
		s.mu.Unlock()
	}
	if err := s.messenger.AppendStream(s.ctx, s.req.Channel, ts, []map[string]any{{"type": "markdown_text", "text": text}}); err != nil {
		log.Printf("v2 Slack answer stream append failed: %v", err)
		return
	}
	s.mu.Lock()
	s.streamed = true
	s.mu.Unlock()
}
func (s *slackStream) Complete(final string) {
	s.mu.Lock()
	ts, streamed := s.ts, s.streamed
	s.mu.Unlock()
	if ts != "" {
		if !streamed && final != "" {
			_ = s.messenger.AppendStream(context.Background(), s.req.Channel, ts, []map[string]any{{"type": "markdown_text", "text": final}})
		}
		_ = s.messenger.StopStream(context.Background(), s.req.Channel, ts)
		s.clearStatus()
		return
	}
	s.clearStatus()
	if final != "" {
		_, _ = s.messenger.PostMessage(context.Background(), s.req.Channel, s.req.ThreadTS, final)
	}
}
func (s *slackStream) Fail(message string, canceled bool) {
	s.mu.Lock()
	ts := s.ts
	s.mu.Unlock()
	if canceled {
		message = "Cancelled this request."
		if agent.DetectLocale(s.req.Text) == agent.LocaleZH {
			message = "已中止本次请求。"
		}
	}
	if ts != "" {
		_ = s.messenger.StopStream(context.Background(), s.req.Channel, ts)
	}
	s.clearStatus()
	if message != "" {
		_, _ = s.messenger.PostMessage(context.Background(), s.req.Channel, s.req.ThreadTS, message)
	}
}

func (s *slackStream) clearStatus() {
	if s.status != nil {
		_ = s.status.SetThreadStatus(context.Background(), s.req.Channel, s.req.ThreadTS, "", nil)
	}
}

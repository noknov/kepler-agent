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

	"github.com/noknov/slack-copilot-agent/packages/agent/hosted"
	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/slack-copilot-agent/packages/agent/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/session"
	"github.com/noknov/slack-copilot-agent/packages/slackconversation"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

type ConversationMode string

const (
	ModeSteer ConversationMode = "steer"
	ModeQueue ConversationMode = "queue"
)

type Service struct {
	Agent            hosted.Agent
	Messenger        slackconversation.Messenger
	Prompt           safety.PromptPolicy
	Redactor         safety.Redactor
	UserPrefs        userprefs.Store
	Format           slackconversation.TextFormatter
	Workspace        string
	Redis            *redisclient.Client
	PodID            string
	Lifecycle        context.Context
	ModeForUser      func(string) ConversationMode
	ModelFor         func(slackconversation.Request) string
	Multimodal       func(string) bool
	WebSearchEnabled func(string) bool
	Locker           session.Locker
	Queue            QueueStore

	mu     sync.Mutex
	active map[string]*activeRun
	router *eventRouter
}

type QueueStore interface {
	Enqueue(context.Context, string, slackconversation.Request) error
	Drain(context.Context, string) ([]slackconversation.Request, error)
}

type RedisQueue struct {
	Client *redisclient.Client
	TTL    time.Duration
}

func (q RedisQueue) Enqueue(ctx context.Context, sessionID string, request slackconversation.Request) error {
	if strings.TrimSpace(request.EventID) == "" {
		request.EventID = fmt.Sprintf("queued-%d", time.Now().UnixNano())
	}
	return q.Client.EnqueueJSON(ctx, "agent:queue:"+sessionID, request.EventID, request, q.TTL)
}

func (q RedisQueue) Drain(ctx context.Context, sessionID string) ([]slackconversation.Request, error) {
	values, err := q.Client.DrainJSONQueue(ctx, "agent:queue:"+sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]slackconversation.Request, 0, len(values))
	for _, value := range values {
		var request slackconversation.Request
		if json.Unmarshal(value, &request) == nil {
			out = append(out, request)
		}
	}
	return out, nil
}

type activeRun struct {
	userID   string
	cancel   context.CancelFunc
	steering *agentruntime.InputBuffer
	mu       sync.Mutex
	queued   []slackconversation.Request
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
	if event.Type == transcript.ModelStreamed && event.Model != nil && event.Model.Type == model.StreamTextDelta && event.Model.Text != "" {
		stream.Write(event.Model.Text)
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

func (s *Service) HandleMention(ctx context.Context, req slackconversation.Request) bool {
	return s.handle(ctx, req)
}
func (s *Service) HandleReply(ctx context.Context, req slackconversation.Request) bool {
	if s.Messenger == nil || s.Agent.Runtime == nil {
		return false
	}
	sessionID := session.ID(req.Channel, req.ThreadTS)
	if s.controlActive(sessionID, req) {
		return true
	}
	waiting, err := s.Agent.Runtime.WaitingForInput(ctx, sessionID, req.UserID)
	if err != nil || !waiting {
		return false
	}
	s.run(ctx, sessionID, req)
	return true
}

func (s *Service) handle(ctx context.Context, req slackconversation.Request) bool {
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

func (s *Service) run(eventCtx context.Context, sessionID string, req slackconversation.Request) {
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
			_, _ = s.Messenger.PostMessage(context.Background(), req.Channel, req.ThreadTS, "Unable to lock this conversation: "+s.Redactor.Sanitize(err.Error()))
			return
		}
		defer unlock()
	}
	if queued := s.drainQueue(sessionID, nil); len(queued) > 0 {
		req = combine(append(queued, req))
	}
	active := &activeRun{userID: req.UserID, cancel: cancel, steering: &agentruntime.InputBuffer{}}
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
		if queued := s.drainQueue(sessionID, active); len(queued) > 0 {
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
	stream.sanitize = s.Redactor.Sanitize
	s.router.set(turnID, stream)
	defer s.router.set(turnID, nil)
	stream.Start()

	threadContext := s.Messenger.ThreadContext(runCtx, req.Channel, req.ThreadTS, 0)
	fragments := []prompt.Fragment{
		{ID: "hosted-core", Version: "1", Layer: prompt.LayerCore, Content: s.Prompt.SystemPrompt()},
		{ID: "slack-progress", Version: "1", Layer: prompt.LayerProduct, Content: prompts.ToolStatus("instruction", defaultStatusInstruction)},
		{ID: "user-rules", Layer: prompt.LayerUser, Content: userprefs.RulesPrompt(runCtx, s.UserPrefs, req.UserID)},
		{ID: "user-skills", Layer: prompt.LayerSkill, Content: userprefs.SkillsMetadataPrompt(runCtx, s.UserPrefs, req.UserID)},
	}
	if strings.TrimSpace(threadContext) != "" {
		fragments = append(fragments, prompt.Fragment{ID: "slack-thread", Layer: prompt.LayerEnvironment, Content: "Slack thread context supplied by the transport:\n" + threadContext})
	}
	modelName := ""
	if s.ModelFor != nil {
		modelName = s.ModelFor(req)
	}
	webSearch := "enabled"
	if s.WebSearchEnabled != nil && !s.WebSearchEnabled(req.UserID) {
		webSearch = "disabled"
	}
	input := req.Message()
	if s.Multimodal != nil && !s.Multimodal(modelName) {
		input = withoutUnsupportedImages(input, slackconversation.IsCJK(req.Text))
	}
	result, err := s.Agent.Run(runCtx, hosted.Request{SessionID: sessionID, TurnID: turnID, UserID: req.UserID, Workspace: s.Workspace, Input: input, Model: modelName, Steering: active.steering, Prompt: fragments, ScopeValues: map[string]string{"channel": req.Channel, "thread_ts": req.ThreadTS, "web_search": webSearch}})
	if err != nil {
		stream.Fail(s.Redactor.Sanitize(err.Error()), errors.Is(err, context.Canceled))
		return
	}
	final := s.Redactor.Sanitize(renderAnswer(result.Message))
	if s.Format != nil {
		final = s.Format(final)
	}
	stream.Complete(final)
}

func renderAnswer(message model.Message) string {
	answer := strings.TrimSpace(message.Text())
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

func (s *Service) mode(userID string) ConversationMode {
	if s.ModeForUser != nil {
		if mode := s.ModeForUser(userID); mode == ModeQueue {
			return mode
		}
	}
	return ModeSteer
}

func (s *Service) controlActive(sessionID string, req slackconversation.Request) bool {
	s.mu.Lock()
	active := s.active[sessionID]
	s.mu.Unlock()
	if active != nil && active.userID == req.UserID {
		if isCancel(req.Text) {
			active.cancel()
			return true
		}
		if s.mode(req.UserID) == ModeQueue {
			s.enqueue(sessionID, active, req)
		} else {
			active.steering.Push(model.TextMessage(model.RoleUser, steeringText(req)))
		}
		return true
	}
	if s.Redis == nil || strings.TrimSpace(req.ThreadTS) == "" {
		return false
	}
	owner, err := s.Redis.Get(context.Background(), "agent:active:"+sessionID)
	if err != nil || owner == "" || owner == s.PodID {
		return false
	}
	action := string(s.mode(req.UserID))
	if isCancel(req.Text) {
		action = "cancel"
	}
	payload, _ := json.Marshal(control{Session: sessionID, Action: action, Request: req})
	queuedPersisted := false
	if action == string(ModeQueue) {
		if s.Queue == nil || s.Queue.Enqueue(context.Background(), sessionID, req) != nil {
			return false
		}
		queuedPersisted = true
	}
	count, err := s.Redis.PublishCount(context.Background(), "agent:control:"+owner, string(payload))
	return queuedPersisted || (err == nil && count > 0)
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

type control struct {
	Session string                    `json:"session"`
	Action  string                    `json:"action"`
	Request slackconversation.Request `json:"request"`
}

func (s *Service) StartControlSubscriber(ctx context.Context) {
	if s.Redis == nil || s.PodID == "" {
		return
	}
	sub := s.Redis.Subscribe(ctx, "agent:control:"+s.PodID)
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
				if s.Queue == nil {
					active.enqueue(cmd.Request)
				}
			default:
				active.steering.Push(model.TextMessage(model.RoleUser, steeringText(cmd.Request)))
			}
		}
	}
}

func (s *Service) enqueue(sessionID string, active *activeRun, request slackconversation.Request) {
	if s.Queue != nil && s.Queue.Enqueue(context.Background(), sessionID, request) == nil {
		return
	}
	active.enqueue(request)
}

func (s *Service) drainQueue(sessionID string, active *activeRun) []slackconversation.Request {
	var out []slackconversation.Request
	if s.Queue != nil {
		if queued, err := s.Queue.Drain(context.Background(), sessionID); err == nil {
			out = append(out, queued...)
		}
	}
	if active != nil {
		out = append(out, active.drainQueue()...)
	}
	return out
}

func (a *activeRun) enqueue(req slackconversation.Request) {
	a.mu.Lock()
	a.queued = append(a.queued, req)
	a.mu.Unlock()
}
func (a *activeRun) drainQueue() []slackconversation.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := append([]slackconversation.Request(nil), a.queued...)
	a.queued = nil
	return out
}

func combine(requests []slackconversation.Request) slackconversation.Request {
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

func steeringText(req slackconversation.Request) string {
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
	ctx            context.Context
	messenger      slackconversation.Messenger
	req            slackconversation.Request
	mu             sync.Mutex
	ts             string
	pending        strings.Builder
	streamed       bool
	deliveryFailed bool
	status         slackconversation.ThreadStatusMessenger
	lastStatus     string
	sanitize       func(string) string
}

func newSlackStream(ctx context.Context, messenger slackconversation.Messenger, req slackconversation.Request) *slackStream {
	return &slackStream{ctx: ctx, messenger: messenger, req: req}
}
func (s *slackStream) Start() {
	status, ok := s.messenger.(slackconversation.ThreadStatusMessenger)
	if !ok {
		return
	}
	s.status = status
}
func (s *slackStream) Write(text string) {
	s.mu.Lock()
	s.pending.WriteString(text)
	s.mu.Unlock()
}
func (s *slackStream) CommitStep(message model.Message) {
	s.mu.Lock()
	text := s.pending.String()
	s.pending.Reset()
	ts := s.ts
	s.mu.Unlock()
	if text == "" {
		text = message.Text()
	}
	if len(message.ToolCalls()) > 0 {
		s.StepSummary(text)
		return
	}
	if text == "" {
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
		log.Printf("Slack answer stream append failed: %v", err)
		s.mu.Lock()
		s.deliveryFailed = true
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.streamed = true
	s.mu.Unlock()
}
func (s *slackStream) Complete(final string) {
	s.mu.Lock()
	ts, streamed, deliveryFailed := s.ts, s.streamed, s.deliveryFailed
	s.mu.Unlock()
	if ts != "" {
		if !streamed && final != "" {
			if err := s.messenger.AppendStream(context.Background(), s.req.Channel, ts, []map[string]any{{"type": "markdown_text", "text": final}}); err != nil {
				deliveryFailed = true
			}
		}
		_ = s.messenger.StopStream(context.Background(), s.req.Channel, ts)
		s.clearStatus()
		if deliveryFailed && final != "" {
			_, _ = s.messenger.PostMessage(context.Background(), s.req.Channel, s.req.ThreadTS, final)
		}
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
		if slackconversation.IsCJK(s.req.Text) {
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
		s.mu.Lock()
		if s.lastStatus == "\x00" {
			s.mu.Unlock()
			return
		}
		s.lastStatus = "\x00"
		s.mu.Unlock()
		_ = s.status.SetThreadStatus(context.Background(), s.req.Channel, s.req.ThreadTS, "", nil)
	}
}

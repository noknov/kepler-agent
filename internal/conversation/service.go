package conversation

import (
	"context"
	"sync"
	"time"

	"github.com/wati/oncall-agent/internal/agent"
	"github.com/wati/oncall-agent/internal/memory"
	"github.com/wati/oncall-agent/internal/observability"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/session"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type Messenger interface {
	PostMessage(ctx context.Context, channel, threadTS, text string) (string, error)
	DeleteMessage(ctx context.Context, channel, ts string) error
	ThreadContext(ctx context.Context, channel, threadTS string, limit int) string
}

type Service struct {
	Store     session.Store
	Messenger Messenger
	Runner    agent.Runner
	Memory    memory.Builder
	Prompt    safety.PromptPolicy
	Redactor  safety.Redactor
	Metrics   *observability.Recorder

	mu       sync.Mutex
	locks    map[string]*sync.Mutex
	seen     map[string]time.Time
	seenTTL  time.Duration
	maxTurns int
}

type Request struct {
	EventID  string
	UserID   string
	Channel  string
	ThreadTS string
	Text     string
}

func NewService(store session.Store, messenger Messenger, runner agent.Runner, memoryBuilder memory.Builder, prompt safety.PromptPolicy, redactor safety.Redactor, metrics *observability.Recorder) *Service {
	return &Service{
		Store:     store,
		Messenger: messenger,
		Runner:    runner,
		Memory:    memoryBuilder,
		Prompt:    prompt,
		Redactor:  redactor,
		Metrics:   metrics,
		locks:     map[string]*sync.Mutex{},
		seen:      map[string]time.Time{},
		seenTTL:   10 * time.Minute,
		maxTurns:  memoryBuilder.MaxMessages * 2,
	}
}

func (s *Service) HandleMention(ctx context.Context, req Request) {
	s.process(ctx, req, false)
}

func (s *Service) HandleReply(ctx context.Context, req Request) {
	s.process(ctx, req, true)
}

func (s *Service) process(ctx context.Context, req Request, requirePending bool) {
	if s.Store == nil || s.Messenger == nil {
		return
	}
	sessionID := session.ID(req.Channel, req.ThreadTS)
	if !s.markEvent(req.EventID) {
		return
	}
	lock := s.lockFor(sessionID)
	lock.Lock()
	defer lock.Unlock()

	if s.Metrics != nil {
		s.Metrics.Request()
	}

	sess, ok, err := s.Store.Get(ctx, sessionID)
	if err != nil {
		s.reportError(ctx, req, "读取会话失败："+s.Redactor.Sanitize(err.Error()))
		return
	}
	if requirePending {
		if !ok || !sess.PendingUserInput || sess.PendingUserID != req.UserID {
			return
		}
	}
	if !ok {
		sess = session.Session{ID: sessionID, Channel: req.Channel, ThreadTS: req.ThreadTS, UserID: req.UserID}
	}

	start := time.Now()
	thinkingTS, _ := s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, "<@"+req.UserID+"> :thinking_face: thinking...")
	defer func() {
		if s.Metrics != nil {
			s.Metrics.Latency(time.Since(start))
		}
		if thinkingTS != "" {
			_ = s.Messenger.DeleteMessage(context.Background(), req.Channel, thinkingTS)
		}
	}()

	threadContext := s.Messenger.ThreadContext(ctx, req.Channel, req.ThreadTS, 30)
	messages := s.Memory.Build(
		s.Prompt.SystemPrompt(),
		threadContext,
		req.Text,
		sess.Summary,
		sess.Turns,
	)
	result, err := s.Runner.Run(ctx, agent.Request{
		Messages: messages,
		Runtime: registry.Runtime{
			UserID:   req.UserID,
			Channel:  req.Channel,
			ThreadTS: req.ThreadTS,
		},
	})

	sess.UserID = req.UserID
	sess.PendingUserInput = false
	sess.PendingUserID = ""
	sess.PendingQuestion = ""
	sess.Turns = append(sess.Turns, memory.UserTurn(req.Text))
	sess.Turns = append(sess.Turns, memory.FromLLM(result.Generated)...)

	if err != nil {
		if s.Metrics != nil {
			s.Metrics.Error(err)
		}
		sess.Turns = trimTurns(sess.Turns, s.maxTurns)
		_ = s.Store.Save(ctx, sess)
		s.reportError(ctx, req, "<@"+req.UserID+"> 处理失败："+s.Redactor.Sanitize(err.Error()))
		return
	}

	if result.Pending {
		sess.PendingUserInput = true
		sess.PendingUserID = req.UserID
		sess.PendingQuestion = result.PendingQuestion
	}
	sess.Turns = trimTurns(sess.Turns, s.maxTurns)
	if err := s.Store.Save(ctx, sess); err != nil && s.Metrics != nil {
		s.Metrics.Error(err)
	}
	if !result.Pending && result.Final != "" {
		_, _ = s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, result.Final)
	}
}

func (s *Service) reportError(ctx context.Context, req Request, text string) {
	_, _ = s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, text)
}

func (s *Service) lockFor(sessionID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock := s.locks[sessionID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[sessionID] = lock
	}
	return lock
}

func (s *Service) markEvent(eventID string) bool {
	if eventID == "" {
		return true
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, seenAt := range s.seen {
		if now.Sub(seenAt) > s.seenTTL {
			delete(s.seen, id)
		}
	}
	if _, ok := s.seen[eventID]; ok {
		return false
	}
	s.seen[eventID] = now
	return true
}

func trimTurns(turns []memory.Turn, max int) []memory.Turn {
	if max <= 0 || len(turns) <= max {
		return turns
	}
	return append([]memory.Turn(nil), turns[len(turns)-max:]...)
}

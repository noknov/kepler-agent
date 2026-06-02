package conversation

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/wati/oncall-agent/internal/agent"
	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/memory"
	"github.com/wati/oncall-agent/internal/observability"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/session"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type Messenger interface {
	PostMessage(ctx context.Context, channel, threadTS, text string) (string, error)
	StartStream(ctx context.Context, channel, threadTS, recipientUserID string) (string, error)
	AppendStream(ctx context.Context, channel, ts string, chunks []map[string]any) error
	StopStream(ctx context.Context, channel, ts string) error
	DeleteMessage(ctx context.Context, channel, ts string) error
	ThreadContext(ctx context.Context, channel, threadTS string, limit int) string
}

type TextFormatter func(string) string

type Service struct {
	Store     session.Store
	Messenger Messenger
	Runner    agent.Runner
	Memory    memory.Builder
	Prompt    safety.PromptPolicy
	Redactor  safety.Redactor
	Metrics   *observability.Recorder
	Format    TextFormatter

	mu       sync.Mutex
	locks    map[string]*sync.Mutex
	seen     map[string]time.Time
	seenTTL  time.Duration
	maxTurns int
}

type Request struct {
	EventID      string
	UserID       string
	Channel      string
	ThreadTS     string
	Text         string
	ContentParts []llm.ContentPart
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
	_ = s.process(ctx, req, false)
}

func (s *Service) HandleReply(ctx context.Context, req Request) bool {
	return s.process(ctx, req, true)
}

func (s *Service) process(ctx context.Context, req Request, requirePending bool) bool {
	if s.Store == nil || s.Messenger == nil {
		return false
	}
	sessionID := session.ID(req.Channel, req.ThreadTS)
	lock := s.lockFor(sessionID)
	lock.Lock()
	defer lock.Unlock()

	sess, ok, err := s.Store.Get(ctx, sessionID)
	if err != nil {
		s.reportError(ctx, req, "Failed to load session: "+s.Redactor.Sanitize(err.Error()))
		return false
	}
	if requirePending {
		if !ok || !sess.PendingUserInput || sess.PendingUserID != req.UserID {
			return false
		}
	}
	if !s.markEvent(req.EventID) {
		return false
	}
	if s.Metrics != nil {
		s.Metrics.Request()
	}
	if !ok {
		sess = session.Session{ID: sessionID, Channel: req.Channel, ThreadTS: req.ThreadTS, UserID: req.UserID}
	}
	sess.Turns = memory.FilterPersistentTurns(sess.Turns)

	start := time.Now()

	streamTS, streamErr := s.Messenger.StartStream(ctx, req.Channel, req.ThreadTS, req.UserID)
	useStream := streamErr == nil && streamTS != ""
	if streamErr != nil {
		log.Printf("stream fallback: %v", streamErr)
	}

	var thinkingTS string
	if !useStream {
		thinkingTS, _ = s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, ":thinking_face: ...")
	}
	defer func() {
		if s.Metrics != nil {
			s.Metrics.Latency(time.Since(start))
		}
		if useStream {
			_ = s.Messenger.StopStream(context.Background(), req.Channel, streamTS)
		} else if thinkingTS != "" {
			_ = s.Messenger.DeleteMessage(context.Background(), req.Channel, thinkingTS)
		}
	}()

	const taskID = "thinking"
	runner := s.Runner
	runner.StatusUpdate = func(status string) {
		if !useStream {
			return
		}
		_ = s.Messenger.AppendStream(ctx, req.Channel, streamTS, []map[string]any{
			{"type": "task_update", "id": taskID, "title": status, "status": "in_progress"},
		})
	}

	threadContext := s.Messenger.ThreadContext(ctx, req.Channel, req.ThreadTS, 30)
	messages := s.Memory.BuildWithParts(
		s.Prompt.SystemPrompt(),
		threadContext,
		req.Text,
		req.ContentParts,
		sess.Summary,
		sess.Turns,
	)
	result, err := runner.Run(ctx, agent.Request{
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

	if err != nil {
		if s.Metrics != nil {
			s.Metrics.Error(err)
		}
		sess.Turns = trimTurns(sess.Turns, s.maxTurns)
		_ = s.Store.Save(ctx, sess)
		errMsg := s.Redactor.Sanitize(userFacingError(err))
		if useStream {
			_ = s.Messenger.AppendStream(ctx, req.Channel, streamTS, []map[string]any{
				{"type": "task_update", "id": taskID, "status": "error"},
				{"type": "markdown_text", "text": errMsg},
			})
		} else {
			s.reportError(ctx, req, errMsg)
		}
		return true
	}

	sess.Turns = append(sess.Turns, memory.FilterPersistentTurns(memory.FromLLM(result.Generated))...)

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
		finalText := s.Redactor.Sanitize(result.Final)
		if useStream {
			_ = s.Messenger.AppendStream(ctx, req.Channel, streamTS, []map[string]any{
				{"type": "task_update", "id": taskID, "status": "complete"},
				{"type": "markdown_text", "text": finalText},
			})
		} else {
			text := finalText
			if s.Format != nil {
				text = s.Format(text)
			}
			_, _ = s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, text)
		}
	}
	return true
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
	start := len(turns) - max
	for start < len(turns) && turns[start].Role == memory.RoleTool {
		start++
	}
	if start >= len(turns) {
		return nil
	}
	return append([]memory.Turn(nil), turns[start:]...)
}

func userFacingError(err error) string {
	switch {
	case errors.Is(err, agent.ErrRepetitiveOutput):
		return "模型输出出现了重复循环，我已经中断这次回复，避免把无效内容继续发到 Slack。请换一种更具体的问法再试一次。"
	case errors.Is(err, agent.ErrRepeatedToolCall):
		return "模型重复调用了同一个工具，我已经中断这次分析，避免继续消耗 token。请稍微缩小问题范围后再试一次。"
	case errors.Is(err, agent.ErrTextualToolCall):
		return "模型返回了文本形式的工具调用而不是结构化工具接口，无法安全执行。请换一种更具体的问法，或稍后再试。"
	default:
		return llm.UserFacingError(err)
	}
}

package conversation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/wati/oncall-agent/internal/agent"
	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/memory"
	"github.com/wati/oncall-agent/internal/observability"
	"github.com/wati/oncall-agent/internal/runs"
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
	Store       session.Store
	Messenger   Messenger
	Runner      agent.Runner
	Memory      memory.Builder
	Prompt      safety.PromptPolicy
	Redactor    safety.Redactor
	Metrics     *observability.Recorder
	Format      TextFormatter
	RunStore    runs.Store
	RunProvider string
	RunModel    string
	CostRates   observability.CostRates

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
	runObserver := s.newRunObserver(sessionID, req, start)

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
	locale := agent.DetectLocale(req.Text)

	runner := s.Runner
	if runObserver != nil {
		runner.Observer = multiObserver{s.Metrics, runObserver}
	}
	runner.StatusUpdate = func(status string) {
		if !useStream {
			return
		}
		_ = s.Messenger.AppendStream(ctx, req.Channel, streamTS, []map[string]any{
			{"type": "task_update", "id": taskID, "title": status, "status": "in_progress"},
		})
	}

	var flusher *streamFlusher
	if useStream {
		flusher = &streamFlusher{
			ctx: ctx, messenger: s.Messenger,
			channel: req.Channel, streamTS: streamTS,
			taskID: taskID, locale: locale,
		}
		runner.OnToken = flusher.Write
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
		Locale: locale,
	})
	if flusher != nil {
		flusher.Flush()
	}

	sess.UserID = req.UserID
	sess.PendingUserInput = false
	sess.PendingUserID = ""
	sess.PendingQuestion = ""
	sess.Turns = append(sess.Turns, memory.UserTurn(req.Text))

	if err != nil {
		errorID := newErrorID()
		log.Printf("conversation error id=%s session=%s event=%s user=%s channel=%s thread=%s err=%v", errorID, sessionID, req.EventID, req.UserID, req.Channel, req.ThreadTS, err)
		if runObserver != nil {
			runObserver.Finish("error", errorID, err, "")
		}
		if s.Metrics != nil {
			s.Metrics.Error(wrapErrorID(errorID, err))
		}
		sess.Turns, sess.Summary = s.trimAndSummarize(sess.Turns, sess.Summary)
		_ = s.Store.Save(ctx, sess)
		errMsg := s.Redactor.Sanitize(userFacingError(errorID))
		if useStream {
			_ = s.Messenger.AppendStream(ctx, req.Channel, streamTS, []map[string]any{
				{"type": "task_update", "id": taskID, "title": agent.FailedTitle(locale), "status": "error"},
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
	sess.Turns, sess.Summary = s.trimAndSummarize(sess.Turns, sess.Summary)
	if err := s.Store.Save(ctx, sess); err != nil && s.Metrics != nil {
		s.Metrics.Error(err)
	}
	if result.Pending && result.PendingQuestion != "" {
		pendingText := s.Redactor.Sanitize(result.PendingQuestion)
		if s.Format != nil {
			pendingText = s.Format(pendingText)
		}
		if useStream {
			_ = s.Messenger.AppendStream(ctx, req.Channel, streamTS, []map[string]any{
				{"type": "task_update", "id": taskID, "title": agent.WaitingTitle(locale), "status": "complete"},
			})
		}
		ts, _ := s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, "<@"+req.UserID+"> "+pendingText)
		if runObserver != nil {
			runObserver.LinkSlackMessage(req.Channel, ts)
		}
	}
	if !result.Pending && result.Final != "" {
		finalText := s.Redactor.Sanitize(result.Final)
		if runObserver != nil {
			runObserver.Finish("completed", "", nil, finalText)
		}
		if useStream {
			if !result.Streamed {
				if s.Format != nil {
					finalText = s.Format(finalText)
				}
				_ = s.Messenger.AppendStream(ctx, req.Channel, streamTS, []map[string]any{
					{"type": "task_update", "id": taskID, "title": agent.CompleteTitle(locale), "status": "complete"},
					{"type": "markdown_text", "text": finalText},
				})
			}
			if runObserver != nil {
				runObserver.LinkSlackMessage(req.Channel, streamTS)
			}
		} else {
			if s.Format != nil {
				finalText = s.Format(finalText)
			}
			ts, _ := s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, finalText)
			if runObserver != nil {
				runObserver.LinkSlackMessage(req.Channel, ts)
			}
		}
	} else if result.Pending && runObserver != nil {
		runObserver.Finish("pending_user", "", nil, result.PendingQuestion)
	}
	return true
}

func (s *Service) newRunObserver(sessionID string, req Request, startedAt time.Time) *runs.Observer {
	if s.RunStore == nil {
		return nil
	}
	return runs.NewObserver(s.RunStore, runs.Run{
		ID:        runs.NewID(),
		SessionID: sessionID,
		EventID:   req.EventID,
		UserID:    req.UserID,
		Channel:   req.Channel,
		ThreadTS:  req.ThreadTS,
		Provider:  s.RunProvider,
		Model:     s.RunModel,
		Status:    "running",
		StartedAt: startedAt.UTC(),
	}, s.CostRates)
}

type multiObserver []agent.Observer

func (m multiObserver) LLMCall(usage llm.Usage, d time.Duration, err error) {
	for _, observer := range m {
		if observer != nil {
			observer.LLMCall(usage, d, err)
		}
	}
}

func (m multiObserver) ToolCall(name string, d time.Duration, err error) {
	for _, observer := range m {
		if observer != nil {
			observer.ToolCall(name, d, err)
		}
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
	start := len(turns) - max
	for start < len(turns) && turns[start].Role == memory.RoleTool {
		start++
	}
	if start >= len(turns) {
		return nil
	}
	return append([]memory.Turn(nil), turns[start:]...)
}

func (s *Service) trimAndSummarize(turns []memory.Turn, existing string) ([]memory.Turn, string) {
	trimmed := trimTurns(turns, s.maxTurns)
	removed := len(turns) - len(trimmed)
	if removed <= 0 {
		return trimmed, existing
	}
	addition := summarizeTurns(turns[:removed])
	if addition == "" {
		return trimmed, existing
	}
	summary := strings.TrimSpace(existing)
	if summary != "" {
		summary += "\n"
	}
	summary += addition
	return trimmed, trimSummary(summary, s.Memory.MaxSummaryChars)
}

func summarizeTurns(turns []memory.Turn) string {
	lines := make([]string, 0, len(turns)+1)
	lines = append(lines, "Trimmed conversation summary:")
	for _, turn := range turns {
		label := string(turn.Role)
		content := strings.TrimSpace(turn.Content)
		if turn.Role == memory.RoleAssistant && len(turn.ToolCalls) > 0 {
			names := make([]string, 0, len(turn.ToolCalls))
			for _, call := range turn.ToolCalls {
				names = append(names, call.Name)
			}
			content = "called tools: " + strings.Join(names, ", ")
		}
		if turn.Role == memory.RoleTool {
			label = "tool:" + turn.Name
		}
		content = strings.Join(strings.Fields(content), " ")
		if len(content) > 240 {
			content = content[:240] + "..."
		}
		if content != "" {
			lines = append(lines, "- "+label+": "+content)
		}
	}
	return strings.Join(lines, "\n")
}

func trimSummary(summary string, max int) string {
	summary = strings.TrimSpace(summary)
	if max <= 0 || len(summary) <= max {
		return summary
	}
	return strings.TrimSpace(summary[len(summary)-max:])
}

func userFacingError(errorID string) string {
	return "Something went wrong. Please try again later. Error ID: " + errorID
}

func newErrorID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "err-" + time.Now().UTC().Format("20060102150405")
	}
	return "err-" + hex.EncodeToString(b[:])
}

func wrapErrorID(errorID string, err error) error {
	return &errorWithID{id: errorID, err: err}
}

type errorWithID struct {
	id  string
	err error
}

func (e *errorWithID) Error() string {
	return "error_id=" + e.id + " " + e.err.Error()
}

func (e *errorWithID) Unwrap() error {
	return e.err
}

type streamFlusher struct {
	ctx       context.Context
	messenger Messenger
	channel   string
	streamTS  string
	taskID    string
	locale    string
	buf       strings.Builder
	lastFlush time.Time
	started   bool
}

func (f *streamFlusher) Write(delta string) {
	if !f.started {
		f.started = true
		_ = f.messenger.AppendStream(f.ctx, f.channel, f.streamTS, []map[string]any{
			{"type": "task_update", "id": f.taskID, "title": agent.CompleteTitle(f.locale), "status": "complete"},
		})
	}
	f.buf.WriteString(delta)
	if time.Since(f.lastFlush) > 80*time.Millisecond || f.buf.Len() > 80 {
		f.Flush()
	}
}

func (f *streamFlusher) Flush() {
	if f.buf.Len() == 0 {
		return
	}
	text := f.buf.String()
	f.buf.Reset()
	f.lastFlush = time.Now()
	_ = f.messenger.AppendStream(f.ctx, f.channel, f.streamTS, []map[string]any{
		{"type": "markdown_text", "text": text},
	})
}

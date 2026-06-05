package conversation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"runtime/debug"
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
	_ = strings.HasPrefix(req.Channel, "D") // isDM — reserved for future per-channel behavior

	runner := s.Runner
	if runObserver != nil {
		runner.Observer = multiObserver{s.Metrics, runObserver}
	}
	var answerStream *dmStreamWriter
	appendProgress := func(chunks []map[string]any) {
		if !useStream {
			return
		}
		err := s.Messenger.AppendStream(ctx, req.Channel, streamTS, chunks)
		if err == nil {
			return
		}
		if !strings.Contains(err.Error(), "not_in_streaming_state") {
			s.recordDeliveryError(req, streamTS, err)
			return
		}
		newTS, startErr := s.Messenger.StartStream(ctx, req.Channel, req.ThreadTS, req.UserID)
		if startErr != nil {
			s.recordDeliveryError(req, streamTS, startErr)
			return
		}
		streamTS = newTS
		_ = s.Messenger.AppendStream(ctx, req.Channel, streamTS, chunks)
	}
	if useStream {
		runner.OnNarration = func(delta string) {
			appendProgress([]map[string]any{
				{"type": "markdown_text", "text": delta},
			})
		}
		answerStream = &dmStreamWriter{
			ctx: ctx, messenger: s.Messenger,
			channel: req.Channel, threadTS: req.ThreadTS,
			userID: req.UserID,
		}
		runner.OnToken = answerStream.Write
	}
	runner.StatusUpdate = func(status string) {
		appendProgress([]map[string]any{
			{"type": "task_update", "id": taskID, "title": status, "status": "in_progress"},
		})
	}

	threadContext := s.Messenger.ThreadContext(ctx, req.Channel, req.ThreadTS, 0)
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
	if answerStream != nil {
		answerStream.Flush()
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
			runObserver.RecordErrorStack(string(debug.Stack()))
			runObserver.Finish("error", errorID, err, "")
		}
		if s.Metrics != nil {
			s.Metrics.Error(wrapErrorID(errorID, err))
		}
		sess.Turns, sess.Summary = s.trimAndSummarize(sess.Turns, sess.Summary)
		_ = s.Store.Save(ctx, sess)
		errMsg := s.Redactor.Sanitize(userFacingError(errorID))
		if useStream {
			appendErr := s.Messenger.AppendStream(ctx, req.Channel, streamTS, []map[string]any{
				{"type": "task_update", "id": taskID, "title": agent.FailedTitle(locale), "status": "error"},
				{"type": "markdown_text", "text": errMsg},
			})
			if appendErr != nil {
				s.recordDeliveryError(req, streamTS, appendErr)
				s.postFallback(ctx, req, errMsg, false)
			}
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
			appendErr := s.Messenger.AppendStream(ctx, req.Channel, streamTS, []map[string]any{
				{"type": "task_update", "id": taskID, "title": agent.WaitingTitle(locale), "status": "complete"},
			})
			if appendErr != nil {
				s.recordDeliveryError(req, streamTS, appendErr)
			}
		}
		ts, postErr := s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, "<@"+req.UserID+"> "+pendingText)
		if postErr != nil {
			s.recordDeliveryError(req, "", postErr)
		}
		if runObserver != nil {
			runObserver.LinkSlackMessage(req.Channel, ts)
		}
	}
	if !result.Pending && result.Final != "" {
		finalText := s.Redactor.Sanitize(result.Final)
		if runObserver != nil {
			runObserver.Finish("completed", "", nil, finalText)
		}
		// Mark the progress stream as complete.
		appendProgress([]map[string]any{
			{"type": "task_update", "id": taskID, "title": agent.CompleteTitle(locale), "status": "complete"},
		})
		if useStream && answerStream != nil && answerStream.streamTS != "" && !answerStream.Failed() {
			answerStream.Flush()
			_ = s.Messenger.StopStream(ctx, req.Channel, answerStream.streamTS)
			if runObserver != nil {
				runObserver.LinkSlackMessage(req.Channel, answerStream.streamTS)
			}
		} else {
			if s.Format != nil {
				finalText = s.Format(finalText)
			}
			if answerStream.Failed() {
				finalText = "_streaming delivery failed, here is the answer:_\n\n" + finalText
			}
			ts, postErr := s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, finalText)
			if postErr != nil {
				s.recordDeliveryError(req, "", postErr)
			}
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

func (m multiObserver) ToolCallWithMetadata(name string, args json.RawMessage, d time.Duration, err error) {
	for _, observer := range m {
		if observer == nil {
			continue
		}
		if metadataObserver, ok := observer.(agent.MetadataObserver); ok {
			metadataObserver.ToolCallWithMetadata(name, args, d, err)
		} else {
			observer.ToolCall(name, d, err)
		}
	}
}

func (s *Service) reportError(ctx context.Context, req Request, text string) {
	if _, err := s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, text); err != nil {
		s.recordDeliveryError(req, "", err)
	}
}

func (s *Service) postFallback(ctx context.Context, req Request, text string, note bool) (string, bool) {
	fallback := text
	if note {
		fallback = strings.TrimSpace(fallback) + "\n\n_Note: streaming delivery failed, so I reposted the complete answer here._"
	}
	ts, err := s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, fallback)
	if err != nil {
		s.recordDeliveryError(req, "", err)
		return "", false
	}
	return ts, true
}

func (s *Service) recordDeliveryError(req Request, streamTS string, err error) {
	if err == nil {
		return
	}
	target := req.ThreadTS
	if streamTS != "" {
		target = streamTS
	}
	log.Printf("slack delivery error channel=%s thread=%s target=%s event=%s user=%s err=%v", req.Channel, req.ThreadTS, target, req.EventID, req.UserID, err)
	if s.Metrics != nil {
		s.Metrics.Error(fmt.Errorf("slack delivery failed: %w", err))
	}
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
	ctx        context.Context
	messenger  Messenger
	channel    string
	streamTS   string
	taskID     string
	locale     string
	buf        strings.Builder
	lastFlush  time.Time
	started    bool
	contentErr error
}

func (f *streamFlusher) Write(delta string) {
	if !f.started {
		f.started = true
		err := f.messenger.AppendStream(f.ctx, f.channel, f.streamTS, []map[string]any{
			{"type": "task_update", "id": f.taskID, "title": agent.CompleteTitle(f.locale), "status": "complete"},
		})
		if err != nil {
			log.Printf("stream status update failed channel=%s target=%s err=%v", f.channel, f.streamTS, err)
		}
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
	err := f.messenger.AppendStream(f.ctx, f.channel, f.streamTS, []map[string]any{
		{"type": "markdown_text", "text": text},
	})
	f.RecordContentError(err)
}

func (f *streamFlusher) RecordContentError(err error) {
	if err != nil && f.contentErr == nil {
		f.contentErr = err
	}
}

func (f *streamFlusher) Err() error {
	if f == nil {
		return nil
	}
	return f.contentErr
}

// dmStreamWriter lazily opens a new stream message for the final answer.
type dmStreamWriter struct {
	ctx       context.Context
	messenger Messenger
	channel   string
	threadTS  string
	userID    string
	streamTS  string
	buf       strings.Builder
	lastFlush time.Time
	err       error
}

func (w *dmStreamWriter) Write(delta string) {
	if w.err != nil {
		return
	}
	if w.streamTS == "" {
		ts, err := w.messenger.StartStream(w.ctx, w.channel, w.threadTS, w.userID)
		if err != nil {
			log.Printf("answer stream start failed: %v", err)
			w.err = err
			return
		}
		w.streamTS = ts
	}
	w.buf.WriteString(delta)
	if time.Since(w.lastFlush) > 80*time.Millisecond || w.buf.Len() > 80 {
		w.Flush()
	}
}

func (w *dmStreamWriter) Flush() {
	if w.buf.Len() == 0 || w.streamTS == "" {
		return
	}
	text := w.buf.String()
	w.buf.Reset()
	w.lastFlush = time.Now()
	err := w.messenger.AppendStream(w.ctx, w.channel, w.streamTS, []map[string]any{
		{"type": "markdown_text", "text": text},
	})
	if err != nil && w.err == nil {
		w.err = err
	}
}

func (w *dmStreamWriter) Failed() bool {
	return w != nil && w.err != nil
}

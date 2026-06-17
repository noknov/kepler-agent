package conversation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strconv"
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
	Store         session.Store
	Messenger     Messenger
	Runner        agent.Runner
	Memory        memory.Builder
	Prompt        safety.PromptPolicy
	Redactor      safety.Redactor
	Metrics       *observability.Recorder
	Format        TextFormatter
	RunStore      runs.Store
	RunProvider   string
	RunModel      string
	ModelOverride func(userID string) string
	Multimodal    func(model string) bool
	CostRates     observability.CostRates
	HealthSummary func() string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	seen  map[string]time.Time
	// active is intentionally separate from per-session locks: in-flight
	// replies must be queued/cancelled without blocking on the running turn.
	active   map[string]*activeRun
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
		active:    map[string]*activeRun{},
		seenTTL:   10 * time.Minute,
		maxTurns:  memoryBuilder.MaxMessages * 2,
	}
}

func (s *Service) HandleMention(ctx context.Context, req Request) {
	if s.controlActive(req) {
		return
	}
	_ = s.process(ctx, req, false)
}

func (s *Service) HandleReply(ctx context.Context, req Request) bool {
	if s.controlActive(req) {
		return true
	}
	return s.process(ctx, req, true)
}

func (s *Service) process(ctx context.Context, req Request, requirePending bool) bool {
	if s.Store == nil || s.Messenger == nil {
		return false
	}
	sessionID := session.ID(req.Channel, req.ThreadTS)
	lock := s.lockFor(sessionID)
	lock.Lock()
	var followUps []Request
	defer func() {
		lock.Unlock()
		if followUp, ok := combineQueuedFollowUps(followUps); ok {
			go s.process(context.Background(), followUp, false)
		}
	}()

	// The session lock still covers all session load/save and one full agent
	// turn. Active replies are only queued into activeRun and are persisted by
	// this goroutine when drained.
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
	progressTS := streamTS
	progressStopped := false
	defer func() {
		if s.Metrics != nil {
			s.Metrics.Latency(time.Since(start))
		}
		if useStream && !progressStopped {
			_ = s.Messenger.StopStream(context.Background(), req.Channel, progressTS)
		} else if thinkingTS != "" {
			_ = s.Messenger.DeleteMessage(context.Background(), req.Channel, thinkingTS)
		}
	}()

	const taskID = "thinking"
	locale := agent.DetectLocale(req.Text)
	_ = strings.HasPrefix(req.Channel, "D") // isDM — reserved for future per-channel behavior
	runCtx, cancelRun := context.WithCancel(ctx)
	active := newActiveRun(sessionID, req.UserID, cancelRun)
	s.registerActive(sessionID, active)
	defer func() {
		s.unregisterActive(sessionID, active)
		followUps = append(followUps, active.remainingQueued()...)
		cancelRun()
	}()

	runner := s.Runner
	if s.ModelOverride != nil {
		if m := s.ModelOverride(req.UserID); m != "" {
			runner.Model = m
		}
	}
	if runObserver != nil {
		runner.Observer = multiObserver{s.Metrics, runObserver}
	}
	var answerStream *dmStreamWriter
	var progressMarkdown *streamMarkdownBuffer
	var hadNarration bool
	var progressAppendFailed bool
	appendProgress := func(chunks []map[string]any) {
		if !useStream || progressStopped {
			return
		}
		err := s.Messenger.AppendStream(ctx, req.Channel, progressTS, chunks)
		if err == nil {
			return
		}
		for _, chunk := range chunks {
			if chunk["type"] == "markdown_text" {
				progressAppendFailed = true
				break
			}
		}
		if !strings.Contains(err.Error(), "not_in_streaming_state") {
			s.recordDeliveryError(req, progressTS, err)
			return
		}
		newTS, startErr := s.Messenger.StartStream(ctx, req.Channel, req.ThreadTS, req.UserID)
		if startErr != nil {
			s.recordDeliveryError(req, progressTS, startErr)
			return
		}
		progressTS = newTS
		streamTS = newTS
		_ = s.Messenger.AppendStream(ctx, req.Channel, progressTS, chunks)
	}
	appendProgressMarkdown := func(text string) error {
		if !useStream || progressStopped || text == "" {
			return nil
		}
		err := s.Messenger.AppendStream(ctx, req.Channel, progressTS, []map[string]any{
			{"type": "markdown_text", "text": text},
		})
		if err == nil {
			return nil
		}
		progressAppendFailed = true
		if !strings.Contains(err.Error(), "not_in_streaming_state") {
			s.recordDeliveryError(req, progressTS, err)
			return err
		}
		newTS, startErr := s.Messenger.StartStream(ctx, req.Channel, req.ThreadTS, req.UserID)
		if startErr != nil {
			s.recordDeliveryError(req, progressTS, startErr)
			return startErr
		}
		progressTS = newTS
		streamTS = newTS
		return s.Messenger.AppendStream(ctx, req.Channel, progressTS, []map[string]any{
			{"type": "markdown_text", "text": text},
		})
	}
	if useStream {
		progressMarkdown = &streamMarkdownBuffer{
			ctx:     ctx,
			channel: req.Channel,
			append:  appendProgressMarkdown,
			canFlush: func() bool {
				return !progressStopped && progressTS != ""
			},
		}
		defer func() {
			if progressMarkdown != nil {
				progressMarkdown.Close()
			}
		}()
	}
	stopProgress := func() {
		if progressStopped || progressTS == "" {
			return
		}
		progressStopped = true
		_ = s.Messenger.StopStream(context.Background(), req.Channel, progressTS)
	}
	startAnswerStream := func() *dmStreamWriter {
		if answerStream != nil {
			return answerStream
		}
		if progressMarkdown != nil {
			progressMarkdown.Close()
		}
		appendProgress([]map[string]any{
			{"type": "task_update", "id": taskID, "title": agent.CompleteTitle(locale), "status": "complete"},
		})
		stopProgress()
		answerStream = &dmStreamWriter{
			ctx: ctx, messenger: s.Messenger,
			channel: req.Channel, threadTS: req.ThreadTS,
			userID: req.UserID,
		}
		return answerStream
	}
	active.setProgress(locale, appendProgress)
	if useStream {
		runner.OnNarration = func(delta string) {
			hadNarration = true
			progressMarkdown.Write(delta)
		}
		runner.OnToken = func(delta string) {
			if hadNarration {
				startAnswerStream().Write(delta)
				return
			}
			progressMarkdown.Write(delta)
		}
	}
	runner.StatusUpdate = func(status string) {
		appendProgress([]map[string]any{
			{"type": "task_update", "id": taskID, "title": status, "status": "in_progress"},
		})
	}

	contentParts := req.ContentParts
	userText := req.Text
	if len(contentParts) > 0 && s.Multimodal != nil && !s.Multimodal(runner.Model) {
		contentParts, userText = stripImageParts(contentParts, userText, locale)
	}

	threadContext := s.Messenger.ThreadContext(ctx, req.Channel, req.ThreadTS, 0)
	messages := s.Memory.BuildWithParts(
		s.Prompt.SystemPrompt(),
		threadContext,
		userText,
		contentParts,
		sess.Summary,
		sess.Turns,
	)
	result, err := runner.Run(runCtx, agent.Request{
		Messages: messages,
		Runtime: registry.Runtime{
			UserID:   req.UserID,
			Channel:  req.Channel,
			ThreadTS: req.ThreadTS,
		},
		Locale:   locale,
		Steering: s.steering(active),
	})
	if answerStream != nil {
		answerStream.Close()
	}

	sess.UserID = req.UserID
	sess.PendingUserInput = false
	sess.PendingUserID = ""
	sess.PendingQuestion = ""
	sess.Turns = append(sess.Turns, memory.UserTurn(req.Text))
	for _, queued := range active.consumedRequests() {
		if strings.TrimSpace(queued.Text) != "" {
			sess.Turns = append(sess.Turns, memory.UserTurn(queued.Text))
		}
	}

	if err != nil {
		if active.wasCanceled() || errors.Is(err, context.Canceled) {
			if runObserver != nil {
				runObserver.Finish("interrupted", "", nil, "")
			}
			sess.Turns, sess.Summary = s.trimAndSummarize(sess.Turns, sess.Summary)
			_ = s.Store.Save(ctx, sess)
			message := interruptedMessage(locale)
			if useStream {
				appendProgress([]map[string]any{
					{"type": "task_update", "id": taskID, "title": agent.CancelledTitle(locale), "status": "complete"},
					{"type": "markdown_text", "text": message},
				})
			} else {
				s.reportError(ctx, req, message)
			}
			return true
		}
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
			if progressStopped {
				s.postFallback(ctx, req, errMsg, false)
			} else {
				appendProgress([]map[string]any{
					{"type": "task_update", "id": taskID, "title": agent.FailedTitle(locale), "status": "error"},
					{"type": "markdown_text", "text": errMsg},
				})
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
			appendProgress([]map[string]any{
				{"type": "task_update", "id": taskID, "title": agent.WaitingTitle(locale), "status": "complete"},
			})
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
		// Mark the progress stream as complete when the answer stayed on it.
		if !progressStopped {
			appendProgress([]map[string]any{
				{"type": "task_update", "id": taskID, "title": agent.CompleteTitle(locale), "status": "complete"},
			})
		}
		if result.Streamed && answerStream != nil && !answerStream.Failed() {
			answerStreamTS := answerStream.TS()
			if answerStreamTS != "" {
				_ = s.Messenger.StopStream(ctx, req.Channel, answerStreamTS)
			}
			if runObserver != nil && answerStreamTS != "" {
				runObserver.LinkSlackMessage(req.Channel, answerStreamTS)
			}
		}
		if !result.Streamed || progressAppendFailed || (answerStream != nil && answerStream.Failed()) {
			if s.Format != nil {
				finalText = s.Format(finalText)
			}
			if answerStream != nil && answerStream.Failed() {
				finalText = "_streaming delivery failed, here is the answer:_\n\n" + finalText
			} else if progressAppendFailed {
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
	model := s.RunModel
	if s.ModelOverride != nil {
		if m := s.ModelOverride(req.UserID); m != "" {
			model = m
		}
	}
	return runs.NewObserver(s.RunStore, runs.Run{
		ID:        runs.NewID(),
		SessionID: sessionID,
		EventID:   req.EventID,
		UserID:    req.UserID,
		Channel:   req.Channel,
		ThreadTS:  req.ThreadTS,
		Provider:  s.RunProvider,
		Model:     model,
		Status:    "running",
		StartedAt: startedAt.UTC(),
	}, s.CostRates)
}

func (s *Service) steering(active *activeRun) agent.SteeringProvider {
	healthInjected := false
	return func() []llm.Message {
		var messages []llm.Message
		if !healthInjected && s.HealthSummary != nil {
			healthInjected = true
			if summary := strings.TrimSpace(s.HealthSummary()); summary != "" {
				messages = append(messages, llm.Message{Role: "system", Content: summary})
			}
		}
		if active != nil {
			messages = append(messages, active.drainMessages()...)
		}
		return messages
	}
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
	// Prune session locks that are no longer held and have no active run.
	for id, lock := range s.locks {
		if s.active[id] != nil {
			continue
		}
		if lock.TryLock() {
			lock.Unlock()
			delete(s.locks, id)
		}
	}
	if _, ok := s.seen[eventID]; ok {
		return false
	}
	s.seen[eventID] = now
	return true
}

func (s *Service) registerActive(sessionID string, run *activeRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		s.active = map[string]*activeRun{}
	}
	s.active[sessionID] = run
}

func (s *Service) unregisterActive(sessionID string, run *activeRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[sessionID] == run {
		delete(s.active, sessionID)
	}
}

func (s *Service) activeFor(sessionID string) *activeRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[sessionID]
}

func (s *Service) controlActive(req Request) bool {
	if strings.TrimSpace(req.ThreadTS) == "" {
		return false
	}
	active := s.activeFor(session.ID(req.Channel, req.ThreadTS))
	if active == nil || active.userID != req.UserID {
		return false
	}
	if !s.markEvent(req.EventID) {
		return true
	}
	if isCancelRequest(req.Text) {
		active.interrupt()
		return true
	}
	active.enqueue(req)
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
	runes := []rune(summary)
	if max <= 0 || len(runes) <= max {
		return summary
	}
	return strings.TrimSpace(string(runes[len(runes)-max:]))
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

type streamMarkdownBuffer struct {
	ctx      context.Context
	channel  string
	append   func(text string) error
	canFlush func() bool

	mu          sync.Mutex
	flushMu     sync.Mutex
	buf         strings.Builder
	lastFlush   time.Time
	err         error
	loopStarted bool
	wake        chan struct{}
	done        chan struct{}
	closed      bool
}

func (b *streamMarkdownBuffer) Write(delta string) {
	if delta == "" {
		return
	}
	b.mu.Lock()
	if b.err != nil || b.closed {
		b.mu.Unlock()
		return
	}
	if !b.loopStarted {
		b.wake = make(chan struct{}, 1)
		b.done = make(chan struct{})
		b.loopStarted = true
		go b.loop()
	}
	b.buf.WriteString(delta)
	if shouldFlushStream(b.channel, b.lastFlush, b.buf.Len()) {
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
	b.mu.Unlock()
}

func (b *streamMarkdownBuffer) Close() {
	b.mu.Lock()
	if !b.loopStarted || b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	close(b.wake)
	done := b.done
	b.mu.Unlock()
	<-done
}

func (b *streamMarkdownBuffer) Failed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err != nil
}

func (b *streamMarkdownBuffer) loop() {
	b.flush()
	interval, _ := streamFlushConfig(b.channel)
	if interval <= 0 {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(b.done)
	for {
		select {
		case <-b.ctx.Done():
			b.flush()
			return
		case _, ok := <-b.wake:
			b.flush()
			if !ok {
				return
			}
		case <-ticker.C:
			b.flush()
		}
	}
}

func (b *streamMarkdownBuffer) flush() {
	b.flushMu.Lock()
	defer b.flushMu.Unlock()

	b.mu.Lock()
	if b.buf.Len() == 0 || (b.canFlush != nil && !b.canFlush()) {
		b.mu.Unlock()
		return
	}
	text := b.buf.String()
	b.buf.Reset()
	b.lastFlush = time.Now()
	appendFn := b.append
	b.mu.Unlock()

	if appendFn == nil {
		return
	}
	if err := appendFn(text); err != nil {
		b.mu.Lock()
		if b.err == nil {
			b.err = err
		}
		b.mu.Unlock()
	}
}

// dmStreamWriter lazily opens a new stream message for the final answer.
type dmStreamWriter struct {
	ctx       context.Context
	messenger Messenger
	channel   string
	threadTS  string
	userID    string
	streamTS  string
	mu          sync.Mutex
	flushMu     sync.Mutex
	buf         strings.Builder
	lastFlush   time.Time
	err         error
	started     bool
	wake        chan struct{}
	done        chan struct{}
	closed      bool
}

var (
	streamFlushInterval      = envDuration("STREAM_FLUSH_INTERVAL", 35*time.Millisecond)
	streamFlushChars         = envInt("STREAM_FLUSH_CHARS", 32)
	webStreamFlushInterval   = envDuration("WEB_STREAM_FLUSH_INTERVAL", 16*time.Millisecond)
	webStreamFlushChars      = envInt("WEB_STREAM_FLUSH_CHARS", 16)
	slackStreamFlushInterval = envDuration("SLACK_STREAM_FLUSH_INTERVAL", 50*time.Millisecond)
	slackStreamFlushChars    = envInt("SLACK_STREAM_FLUSH_CHARS", 48)
)

func (w *dmStreamWriter) Write(delta string) {
	if delta == "" {
		return
	}
	w.mu.Lock()
	if w.err != nil || w.closed {
		w.mu.Unlock()
		return
	}
	if !w.started {
		w.wake = make(chan struct{}, 1)
		w.done = make(chan struct{})
		w.started = true
		go w.run()
	}
	w.buf.WriteString(delta)
	if shouldFlushStream(w.channel, w.lastFlush, w.buf.Len()) {
		w.signal()
	}
	w.mu.Unlock()
}

func (w *dmStreamWriter) Flush() {
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	w.flushBuffered()
}

func (w *dmStreamWriter) Close() {
	w.mu.Lock()
	if !w.started || w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	close(w.wake)
	done := w.done
	w.mu.Unlock()
	<-done
}

func (w *dmStreamWriter) Failed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err != nil
}

func (w *dmStreamWriter) TS() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.streamTS
}

func (w *dmStreamWriter) run() {
	ts, err := w.messenger.StartStream(w.ctx, w.channel, w.threadTS, w.userID)
	if err != nil {
		log.Printf("answer stream start failed: %v", err)
		w.mu.Lock()
		if w.err == nil {
			w.err = err
		}
		w.mu.Unlock()
		close(w.done)
		return
	}
	w.mu.Lock()
	w.streamTS = ts
	w.mu.Unlock()
	w.flushBuffered()

	interval, _ := streamFlushConfig(w.channel)
	if interval <= 0 {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(w.done)
	for {
		select {
		case <-w.ctx.Done():
			w.flushBuffered()
			return
		case _, ok := <-w.wake:
			w.flushBuffered()
			if !ok {
				return
			}
		case <-ticker.C:
			w.flushBuffered()
		}
	}
}

func (w *dmStreamWriter) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *dmStreamWriter) flushBuffered() {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()

	w.mu.Lock()
	if w.buf.Len() == 0 || w.streamTS == "" {
		w.mu.Unlock()
		return
	}
	text := w.buf.String()
	w.buf.Reset()
	w.lastFlush = time.Now()
	streamTS := w.streamTS
	w.mu.Unlock()

	err := w.messenger.AppendStream(w.ctx, w.channel, streamTS, []map[string]any{
		{"type": "markdown_text", "text": text},
	})
	if err == nil {
		return
	}
	w.mu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.mu.Unlock()
}

func shouldFlushStream(channel string, lastFlush time.Time, bufLen int) bool {
	interval, chars := streamFlushConfig(channel)
	return interval <= 0 || chars <= 0 || time.Since(lastFlush) > interval || bufLen >= chars
}

func streamFlushConfig(channel string) (time.Duration, int) {
	if strings.HasPrefix(channel, "web:") {
		return webStreamFlushInterval, webStreamFlushChars
	}
	return slackStreamFlushInterval, slackStreamFlushChars
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if ms, err := strconv.Atoi(raw); err == nil {
		return time.Duration(ms) * time.Millisecond
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return v
}

func stripImageParts(parts []llm.ContentPart, text, locale string) ([]llm.ContentPart, string) {
	imageCount := 0
	filtered := make([]llm.ContentPart, 0, len(parts))
	for _, p := range parts {
		if p.Type == "image_url" {
			imageCount++
			continue
		}
		filtered = append(filtered, p)
	}
	if imageCount == 0 {
		return parts, text
	}
	note := fmt.Sprintf("\n\n[%d image(s) attached but the current model does not support image input; ask the user to describe the image or paste the text content]", imageCount)
	if locale == agent.LocaleZH {
		note = fmt.Sprintf("\n\n[用户附带了 %d 张图片，但当前模型不支持图片输入；请引导用户描述图片内容或粘贴文字]", imageCount)
	}
	return filtered, text + note
}

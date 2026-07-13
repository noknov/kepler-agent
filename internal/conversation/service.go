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

type ThreadStatusMessenger interface {
	SetThreadStatus(ctx context.Context, channel, threadTS, status string, loadingMessages []string) error
}

type TextFormatter func(string) string

type Service struct {
	Store          session.Store
	Messenger      Messenger
	Runner         agent.Runner
	Compactor      *memory.Compactor
	Memory         memory.Builder
	MemoryPipeline *memory.Pipeline
	Prompt         safety.PromptPolicy
	Redactor       safety.Redactor
	Metrics        *observability.Recorder
	Format         TextFormatter
	RunStore       runs.Store
	RunProvider    string
	RunModel       string
	ModelOverride  func(userID string) string
	Multimodal     func(model string) bool
	// WebSearchEnabled controls whether the web-search tool is available for
	// a given user. When it returns false the tool is excluded from the LLM
	// tool list for that request. When nil, search is always available.
	WebSearchEnabled func(userID string) bool
	CostRates        observability.CostRates
	HealthSummary    func() string
	AutoTTS          AutoTTSFunc
	TTSSummarizer    *TTSSummarizer

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	seen  map[string]time.Time
	// active is intentionally separate from per-session locks: in-flight
	// replies must be queued/cancelled without blocking on the running turn.
	active  map[string]*activeRun
	seenTTL time.Duration
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
		Store:          store,
		Messenger:      messenger,
		Runner:         runner,
		Compactor:      runner.Compactor,
		Memory:         memoryBuilder,
		MemoryPipeline: memory.NewPipeline(memoryBuilder, runner.Compactor),
		Prompt:         prompt,
		Redactor:       redactor,
		Metrics:        metrics,
		locks:          map[string]*sync.Mutex{},
		seen:           map[string]time.Time{},
		active:         map[string]*activeRun{},
		seenTTL:        10 * time.Minute,
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
			// Use a fresh background context with a generous timeout instead of
			// context.Background() so the follow-up can still be cancelled by the
			// server shutdown, and we avoid inheriting a cancelled parent context.
			followCtx, followCancel := context.WithTimeout(context.Background(), 30*time.Minute)
			go func() {
				defer followCancel()
				s.process(followCtx, followUp, false)
			}()
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
	runID := ""
	if runObserver != nil && runObserver.Run != nil {
		runID = runObserver.Run.ID
	}

	statusMessenger, useNativeStatus := s.Messenger.(ThreadStatusMessenger)
	if strings.HasPrefix(req.Channel, "web:") {
		useNativeStatus = false
	}
	var streamTS string
	var streamErr error
	useProgressStream := false
	useStream := useNativeStatus
	var thinkingTS string
	if !useNativeStatus {
		streamTS, streamErr = s.Messenger.StartStream(ctx, req.Channel, req.ThreadTS, req.UserID)
		useProgressStream = streamErr == nil && streamTS != ""
		useStream = useProgressStream
		if streamErr != nil {
			log.Printf("stream fallback: %v", streamErr)
		}
	}

	if !useStream {
		thinkingTS, _ = s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, ":thinking_face: ...")
	}
	progressTS := streamTS
	progressStopped := false
	restartProgressStream := func() bool {
		newTS, err := s.Messenger.StartStream(ctx, req.Channel, req.ThreadTS, req.UserID)
		if err != nil || newTS == "" {
			if err != nil {
				s.recordDeliveryError(req, progressTS, err)
			}
			progressStopped = true
			return false
		}
		progressTS = newTS
		return true
	}
	defer func() {
		if s.Metrics != nil {
			s.Metrics.Latency(time.Since(start))
		}
		if useNativeStatus {
			clearThreadStatus(context.Background(), statusMessenger, req.Channel, req.ThreadTS)
		}
		if useProgressStream && !progressStopped {
			_ = s.Messenger.StopStream(context.Background(), req.Channel, progressTS)
		} else if thinkingTS != "" {
			_ = s.Messenger.DeleteMessage(context.Background(), req.Channel, thinkingTS)
		}
	}()

	const taskID = "thinking"
	// Use session-stored locale for consistency across the thread.
	// Only detect from text on the first message.
	locale := sess.Locale
	if locale == "" {
		locale = agent.DetectLocale(req.Text)
		sess.Locale = locale
	}
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
	runner.Tools = runner.Tools.Clone()
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
	currentStatus := agent.StepStatus(locale, 0)
	// displayedContextTokens is the estimated/current prompt length for the
	// active request. It intentionally excludes cumulative billed usage.
	displayedContextTokens := 0
	var nativeStatus *nativeThreadStatus
	if useNativeStatus && statusMessenger != nil {
		nativeStatus = newNativeThreadStatus(runCtx, statusMessenger, req.Channel, req.ThreadTS, locale, func(err error) {
			s.recordDeliveryError(req, "", err)
		})
	}
	// Keep-alive: Slack clears setStatus after 2 minutes with no activity.
	// Re-send the last known status every 90 seconds to prevent expiry during
	// long-running tool calls.
	if nativeStatus != nil {
		go nativeStatus.keepAlive()
	}
	appendProgress := func(chunks []map[string]any) {
		if useNativeStatus {
			for _, chunk := range chunks {
				if chunk["type"] != "task_update" {
					continue
				}
				status, _ := chunk["status"].(string)
				if status == "complete" {
					clearThreadStatus(context.Background(), statusMessenger, req.Channel, req.ThreadTS)
					continue
				}
				title, _ := chunk["title"].(string)
				if title != "" {
					currentStatus = title
				}
			}
			return
		}
		if !useStream || progressStopped {
			return
		}
		for _, chunk := range chunks {
			if chunk["type"] != "task_update" {
				continue
			}
			if displayedContextTokens <= 0 {
				continue
			}
			title, _ := chunk["title"].(string)
			if title == "" {
				title = currentStatus
			}
			chunk["title"] = streamingTaskTitle(title, displayedContextTokens, s.Memory.MaxContextTokens)
		}
		err := s.Messenger.AppendStream(ctx, req.Channel, progressTS, chunks)
		if err == nil {
			return
		}
		if !isSlackStreamExpired(err) {
			s.recordDeliveryError(req, progressTS, err)
			return
		}
		// Slack streams expire after a few minutes. Open a fresh stream and
		// retry the current chunk so long-running tasks keep showing progress.
		if !restartProgressStream() {
			return
		}
		if retryErr := s.Messenger.AppendStream(ctx, req.Channel, progressTS, chunks); retryErr != nil {
			s.recordDeliveryError(req, progressTS, retryErr)
			progressStopped = true
		}
	}
	appendProgressMarkdown := func(text string) error {
		if useNativeStatus {
			return nil
		}
		if !useStream || progressStopped || text == "" {
			return nil
		}
		err := s.Messenger.AppendStream(ctx, req.Channel, progressTS, []map[string]any{
			{"type": "markdown_text", "text": text},
		})
		if err == nil {
			return nil
		}
		if !isSlackStreamExpired(err) {
			s.recordDeliveryError(req, progressTS, err)
			progressStopped = true
			return err
		}
		if !restartProgressStream() {
			return err
		}
		retryErr := s.Messenger.AppendStream(ctx, req.Channel, progressTS, []map[string]any{
			{"type": "markdown_text", "text": text},
		})
		if retryErr != nil {
			s.recordDeliveryError(req, progressTS, retryErr)
			progressStopped = true
			return retryErr
		}
		return nil
	}
	appendTaskUpdate := func(title, status string) {
		if title != "" {
			currentStatus = title
		}
		appendProgress([]map[string]any{
			{"type": "task_update", "id": taskID, "title": currentStatus, "status": status},
		})
	}
	baseContextTokens := 0
	lastUsageUpdate := time.Time{}
	setCurrentUsage := func(used int) bool {
		if used <= 0 {
			return false
		}
		if displayedContextTokens > 0 && used < displayedContextTokens {
			return false
		}
		displayedContextTokens = used
		return true
	}
	updateLiveUsage := func(delta string) {
		if !useProgressStream || delta == "" {
			return
		}
		if baseContextTokens <= 0 {
			return
		}
		if time.Since(lastUsageUpdate) < 750*time.Millisecond {
			return
		}
		lastUsageUpdate = time.Now()
		if setCurrentUsage(baseContextTokens) {
			appendTaskUpdate(currentStatus, "in_progress")
		}
	}
	updateAPIUsage := func(usage llm.Usage) {
		// Display current prompt/context length, not cumulative billed tokens.
		if !useProgressStream {
			_ = setCurrentUsage(contextTokensFromUsage(usage, baseContextTokens))
			return
		}
		if setCurrentUsage(contextTokensFromUsage(usage, baseContextTokens)) {
			appendTaskUpdate(currentStatus, "in_progress")
		}
	}
	if useProgressStream {
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
		if useNativeStatus {
			clearThreadStatus(context.Background(), statusMessenger, req.Channel, req.ThreadTS)
			progressStopped = true
			return
		}
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
		appendTaskUpdate(agent.GeneratingStatus(locale), "in_progress")
		answerStream = &dmStreamWriter{
			ctx: ctx, messenger: s.Messenger,
			channel: req.Channel, threadTS: req.ThreadTS,
			userID: req.UserID,
		}
		return answerStream
	}
	active.setProgress(locale, appendProgress)
	if useStream {
		runner.OnStream = func(ev agent.StreamEvent) {
			switch ev.Kind {
			case agent.StreamNarration:
				updateLiveUsage(ev.Delta)
			case agent.StreamAnswer:
				updateLiveUsage(ev.Delta)
				startAnswerStream().Write(ev.Delta)
			}
		}
	}
	runner.StatusUpdate = func(status string) {
		if nativeStatus != nil {
			nativeStatus.updateStatic()
			return
		}
		appendTaskUpdate(status, "in_progress")
	}
	runner.LoadingMessageUpdate = func(status string) {
		if nativeStatus != nil {
			nativeStatus.updateLoadingMessage(status)
			return
		}
		appendTaskUpdate(status, "in_progress")
	}
	runner.OnUsage = updateAPIUsage
	runner.OnLLMStepComplete = func() {
		appendTaskUpdate(currentStatus, "in_progress")
	}

	contentParts := req.ContentParts
	userText := req.Text
	if len(contentParts) > 0 && s.Multimodal != nil && !s.Multimodal(runner.Model) {
		contentParts, userText = stripImageParts(contentParts, userText, locale)
	}

	webSearchOff := s.WebSearchEnabled != nil && !s.WebSearchEnabled(req.UserID)
	sysPrompt := s.Prompt.SystemPrompt()
	if webSearchOff {
		sysPrompt += "\n\nThe web-search tool is currently disabled by the user. If this question would clearly benefit from a live web search, politely note in your reply that enabling Auto-search in App Home would allow you to look it up."
	}

	threadContext := s.Messenger.ThreadContext(ctx, req.Channel, req.ThreadTS, 0)
	activeMemory := s.pipeline().BuildActiveRequest(memory.ActiveRequestInput{
		SystemPrompt: sysPrompt,
		ExternalEvidence: []memory.ExternalEvidence{{
			Source:  "slack_thread",
			Content: threadContext,
		}},
		UserText:       userText,
		UserParts:      contentParts,
		SessionSummary: sess.Summary,
		Turns:          sess.Turns,
	})
	messages := activeMemory.Messages
	contentReplacementState := memory.ReconstructContentReplacementState(messages, sess.ContentReplacements)
	baseContextTokens = activeMemory.EstimatedTokens
	if baseContextTokens > 0 {
		setCurrentUsage(baseContextTokens)
	}
	agentReq := agent.Request{
		Messages:     messages,
		UserQuestion: userText,
		Runtime: registry.Runtime{
			UserID:   req.UserID,
			Channel:  req.Channel,
			ThreadTS: req.ThreadTS,
		},
		Locale:                  locale,
		RunID:                   runID,
		Steering:                s.steering(active),
		ContentReplacementState: contentReplacementState,
	}
	if webSearchOff {
		agentReq.DisabledTools = []string{"web-search", "web-read_page"}
	}
	result, err := runner.Run(runCtx, agentReq)
	evidenceText := webEvidenceMarkdown(result.Generated, locale)
	if answerStream != nil && evidenceText != "" {
		answerStream.Write(evidenceText)
	}
	if answerStream != nil {
		answerStream.Close()
	}
	contextUsageTokens := contextUsageTokenCount(messages, result.Generated)
	contextUsage := contextUsageText(s.Memory.MaxContextTokens, contextUsageTokens)
	setCurrentUsage(contextUsageTokens)

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
			sess.Turns, sess.Summary, _ = s.trimAndSummarize(ctx, sess.Turns, sess.Summary)
			_ = s.Store.Save(ctx, sess)
			if useNativeStatus {
				// In native status mode the stream task_update path uses runCtx which is
				// already canceled by the defer; post a real message so the user sees the
				// cancel confirmation. The post itself clears the setStatus indicator.
				s.reportError(ctx, req, interruptedMessage(locale))
			} else if useStream {
				if progressMarkdown != nil {
					progressMarkdown.Close()
				}
				appendProgress([]map[string]any{
					{"type": "task_update", "id": taskID, "title": agent.CancelledTitle(locale), "status": "complete"},
				})
			} else {
				s.reportError(ctx, req, interruptedMessage(locale))
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
		sess.Turns, sess.Summary, _ = s.trimAndSummarize(ctx, sess.Turns, sess.Summary)
		_ = s.Store.Save(ctx, sess)
		if useNativeStatus {
			// In native status mode appendProgress uses runCtx (already
			// canceled by defer). Post a real message so the user receives the error ID.
			// The post also clears the setStatus indicator automatically.
			s.reportError(ctx, req, failedMessageWithErrorID(locale, errorID))
		} else if useStream && !progressStopped {
			appendProgress([]map[string]any{
				{"type": "task_update", "id": taskID, "title": failedTitleWithErrorID(locale, errorID), "status": "error"},
			})
		}
		return true
	}

	sess.Turns = append(sess.Turns, memory.FilterPersistentTurns(memory.FromLLM(result.Generated))...)
	if contentReplacementState != nil {
		sess.ContentReplacements = append([]memory.ContentReplacementRecord(nil), contentReplacementState.Records...)
	}

	if result.Pending {
		sess.PendingUserInput = true
		sess.PendingUserID = req.UserID
		sess.PendingQuestion = result.PendingQuestion
	}
	var compressed bool
	sess.Turns, sess.Summary, compressed = s.trimAndSummarize(ctx, sess.Turns, sess.Summary)
	if err := s.Store.Save(ctx, sess); err != nil && s.Metrics != nil {
		s.Metrics.Error(err)
	}
	if useStream && compressed && !progressStopped {
		appendTaskUpdate(contextCompressingTitle(locale), "in_progress")
	}
	if result.Pending && result.PendingQuestion != "" {
		pendingText := s.Redactor.Sanitize(result.PendingQuestion)
		if s.Format != nil {
			pendingText = s.Format(pendingText)
		}
		if useStream {
			appendTaskUpdate(agent.WaitingTitle(locale), "complete")
			stopProgress()
		}
		if !useStream && contextUsage != "" {
			var notices []string
			notices = append(notices, contextUsage)
			pendingText = appendContextNoticeText(pendingText, notices...)
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
		finalText = appendWebEvidenceText(finalText, evidenceText)
		if runObserver != nil {
			runObserver.Finish("completed", "", nil, finalText)
		}
		answerStreamOK := result.Streamed && answerStream != nil && !answerStream.Failed() && answerStream.TS() != ""
		// In native Slack status mode, keep the status visible until the
		// answer stream is finalized so the UI does not briefly show an empty
		// handoff between thinking and output.
		if !progressStopped && !(useNativeStatus && answerStreamOK) {
			appendTaskUpdate(agent.CompleteTitle(locale), "complete")
			stopProgress()
		}
		if answerStreamOK {
			_ = s.Messenger.StopStream(ctx, req.Channel, answerStream.TS())
			if runObserver != nil {
				runObserver.LinkSlackMessage(req.Channel, answerStream.TS())
			}
			s.maybeAutoTTS(req.Channel, req.ThreadTS, finalText)
		} else {
			if s.Format != nil {
				finalText = s.Format(finalText)
			}
			if !useStream && contextUsage != "" {
				var notices []string
				notices = append(notices, contextUsage)
				finalText = appendContextNoticeText(finalText, notices...)
			}
			ts, postErr := s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, finalText)
			if postErr != nil {
				s.recordDeliveryError(req, "", postErr)
			}
			if runObserver != nil {
				runObserver.LinkSlackMessage(req.Channel, ts)
			}
			s.maybeAutoTTS(req.Channel, req.ThreadTS, finalText)
		}
	} else if result.Pending && runObserver != nil {
		runObserver.Finish("pending_user", "", nil, result.PendingQuestion)
	}
	return true
}

func appendWebEvidenceText(text, evidence string) string {
	text = strings.TrimSpace(text)
	evidence = strings.TrimSpace(evidence)
	if evidence == "" {
		return text
	}
	if text == "" {
		return evidence
	}
	return text + "\n\n" + evidence
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

func contextUsageMarkdown(maxContextTokens int, base, generated []llm.Message) string {
	return contextUsageText(maxContextTokens, contextUsageTokenCount(base, generated))
}

func contextUsageTokenCount(base, generated []llm.Message) int {
	if len(base) == 0 && len(generated) == 0 {
		return 0
	}
	messages := make([]llm.Message, 0, len(base)+len(generated))
	messages = append(messages, base...)
	messages = append(messages, generated...)
	return memory.CountTokensWithCalibration(messages)
}

func contextTokensFromUsage(usage llm.Usage, baseContextTokens int) int {
	// For OpenAI-compatible APIs, CacheReadInputTokens is already included in
	// PromptTokens, so we must not add it again. For Anthropic, cache tokens
	// are independent fields that must be summed in.
	var inputTokens int
	if usage.CacheIncludedInPrompt {
		inputTokens = usage.PromptTokens
	} else {
		inputTokens = usage.PromptTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	}
	if inputTokens > 0 {
		return inputTokens
	}
	if baseContextTokens > 0 {
		return baseContextTokens
	}
	return 0
}

func contextUsageText(maxContextTokens, used int) string {
	if used <= 0 {
		return ""
	}
	limit := maxContextTokens
	if limit <= 0 {
		limit = memory.DefaultMaxContextTokens
	}
	percent := used * 100 / limit
	if percent == 0 {
		percent = 1
	}
	return strconv.Itoa(percent) + "% context"
}

func titleWithContext(title, contextUsage string) string {
	contextUsage = strings.TrimSpace(contextUsage)
	if contextUsage == "" {
		return title
	}
	return title + "    ·    " + contextUsage
}

// streamingTaskTitle builds a task-update title that shows only context-window
// occupancy for the current request, never cumulative billed tokens.
func streamingTaskTitle(status string, ctxTokens, maxCtxTokens int) string {
	var parts []string
	if ctxTokens > 0 {
		limit := maxCtxTokens
		if limit <= 0 {
			limit = memory.DefaultMaxContextTokens
		}
		pct := ctxTokens * 100 / limit
		if pct == 0 {
			pct = 1
		}
		parts = append(parts, strconv.Itoa(pct)+"% context")
	}
	if status != "" {
		parts = append(parts, status)
	}
	return strings.Join(parts, " · ")
}

func completeTitleWithContext(locale, contextUsage string) string {
	return titleWithContext(agent.CompleteTitle(locale), contextUsage)
}

func failedTitleWithErrorID(locale, errorID string) string {
	errorID = strings.TrimSpace(errorID)
	if errorID == "" {
		return agent.FailedTitle(locale)
	}
	return agent.FailedTitle(locale) + " · " + errorID
}

func failedMessageWithErrorID(locale, errorID string) string {
	errorID = strings.TrimSpace(errorID)
	if locale == agent.LocaleZH {
		if errorID == "" {
			return "处理请求时出错，请稍后重试。"
		}
		return "处理请求时出错，请稍后重试。（错误码：" + errorID + "）"
	}
	if errorID == "" {
		return "An error occurred while processing your request. Please try again."
	}
	return "An error occurred while processing your request. (Error ID: " + errorID + ")"
}

func clearThreadStatus(ctx context.Context, messenger ThreadStatusMessenger, channel, threadTS string) {
	if messenger == nil {
		return
	}
	clearCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = messenger.SetThreadStatus(clearCtx, channel, threadTS, "", nil)
}

func appendContextNoticeText(text string, notices ...string) string {
	text = strings.TrimSpace(text)
	var clean []string
	for _, notice := range notices {
		notice = strings.TrimSpace(notice)
		if notice != "" {
			clean = append(clean, "_"+notice+"_")
		}
	}
	if len(clean) == 0 {
		return text
	}
	noticeText := strings.Join(clean, "\n\n")
	if text == "" {
		return noticeText
	}
	return text + "\n\n" + noticeText
}

func streamNotice(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return "\n\n_" + text + "_\n\n"
}

func contextCompressingTitle(locale string) string {
	if locale == agent.LocaleZH {
		return "上下文压缩中..."
	}
	return "Compressing context..."
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

func (m multiObserver) LLMResponse(resp llm.Response, d time.Duration, err error) {
	for _, observer := range m {
		if observer == nil {
			continue
		}
		if responseObserver, ok := observer.(agent.LLMResponseObserver); ok {
			responseObserver.LLMResponse(resp, d, err)
		} else {
			observer.LLMCall(resp.Usage, d, err)
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

func isSlackStreamExpired(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not_in_streaming_state")
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

func (s *Service) trimAndSummarize(ctx context.Context, turns []memory.Turn, existing string) ([]memory.Turn, string, bool) {
	result := s.pipeline().CompactSessionConversation(ctx, turns, existing)
	return result.Turns, result.Summary, result.Compressed
}

func (s *Service) pipeline() *memory.Pipeline {
	if s.MemoryPipeline != nil {
		return s.MemoryPipeline
	}
	return memory.NewPipeline(s.Memory, s.Compactor)
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
	mu        sync.Mutex
	flushMu   sync.Mutex
	buf       strings.Builder
	lastFlush time.Time
	err       error
	started   bool
	wake      chan struct{}
	done      chan struct{}
	closed    bool
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
	if isSlackStreamExpired(err) {
		newTS, startErr := w.messenger.StartStream(w.ctx, w.channel, w.threadTS, w.userID)
		if startErr == nil && newTS != "" {
			w.mu.Lock()
			w.streamTS = newTS
			w.mu.Unlock()
			retryErr := w.messenger.AppendStream(w.ctx, w.channel, newTS, []map[string]any{
				{"type": "markdown_text", "text": text},
			})
			if retryErr == nil {
				return
			}
			err = retryErr
		} else if startErr != nil {
			err = startErr
		}
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

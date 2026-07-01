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
	Compactor     *memory.Compactor
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
	AutoTTS       AutoTTSFunc
	TTSSummarizer *TTSSummarizer

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
		Compactor: runner.Compactor,
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
	runID := ""
	if runObserver != nil && runObserver.Run != nil {
		runID = runObserver.Run.ID
	}

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
	var progressAppendFailed bool
	currentStatus := agent.StepStatus(locale, 0)
	// displayedUsageTokens and cumulativeTokens are declared before appendProgress
	// so the closure can capture them by reference.
	displayedUsageTokens := 0
	priorConversationTokens := s.priorConversationBilledTokens(ctx, sessionID, runObserver)
	cumulativeTokens := 0 // total billed tokens across all LLM calls (provider dashboard number)
	appendProgress := func(chunks []map[string]any) {
		if !useStream || progressStopped {
			return
		}
		for _, chunk := range chunks {
			if chunk["type"] != "task_update" {
				continue
			}
			if cumulativeTokens <= 0 && displayedUsageTokens <= 0 {
				continue
			}
			title, _ := chunk["title"].(string)
			if title == "" {
				title = currentStatus
			}
			chunk["title"] = streamingTaskTitle(title, cumulativeTokens, displayedUsageTokens, s.Memory.MaxContextTokens)
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
		if !strings.Contains(err.Error(), "not_in_streaming_state") {
			progressAppendFailed = true
			s.recordDeliveryError(req, progressTS, err)
			return err
		}
		newTS, startErr := s.Messenger.StartStream(ctx, req.Channel, req.ThreadTS, req.UserID)
		if startErr != nil {
			progressAppendFailed = true
			s.recordDeliveryError(req, progressTS, startErr)
			return startErr
		}
		progressTS = newTS
		streamTS = newTS
		if err := s.Messenger.AppendStream(ctx, req.Channel, progressTS, []map[string]any{
			{"type": "markdown_text", "text": text},
		}); err != nil {
			progressAppendFailed = true
			return err
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
	liveStreamTokens := 0
	lastUsageUpdate := time.Time{}
	setCurrentUsage := func(used int) bool {
		if used <= 0 {
			return false
		}
		if displayedUsageTokens > 0 && used < displayedUsageTokens {
			return false
		}
		displayedUsageTokens = used
		return true
	}
	updateLiveUsage := func(delta string) {
		if !useStream || delta == "" {
			return
		}
		liveStreamTokens += memory.RoughTokenEstimate(delta)
		if baseContextTokens <= 0 || liveStreamTokens <= 0 {
			return
		}
		if time.Since(lastUsageUpdate) < 750*time.Millisecond && liveStreamTokens%250 != 0 {
			return
		}
		lastUsageUpdate = time.Now()
		if setCurrentUsage(baseContextTokens + liveStreamTokens) {
			appendTaskUpdate(currentStatus, "in_progress")
		}
	}
	syncCumulativeTokens := func() {
		if runObserver == nil {
			return
		}
		// Authoritative billing total from completed LLM responses only.
		// Always replace (not max) so any stream-time over-estimate is corrected.
		cumulativeTokens = priorConversationTokens + runObserver.BilledTokens()
	}
	updateAPIUsage := func(usage llm.Usage) {
		// Stream usage events can report inflated or cumulative numbers that do
		// not match the final billed usage recorded by the observer.  Only use
		// stream usage for context-window % display; cumulative billing is
		// synced from the observer after each LLM step completes.
		if setCurrentUsage(contextTokensFromStreamUsage(usage, baseContextTokens)) {
			appendTaskUpdate(currentStatus, "in_progress")
		}
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
		appendTaskUpdate(status, "in_progress")
	}
	runner.OnUsage = updateAPIUsage
	runner.OnLLMStepComplete = func() {
		prev := cumulativeTokens
		syncCumulativeTokens()
		if cumulativeTokens != prev {
			appendTaskUpdate(currentStatus, "in_progress")
		}
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
	baseContextTokens = memory.CountTokensWithCalibration(messages)
	if memory.LastUsage(messages) != nil && baseContextTokens > 0 {
		setCurrentUsage(baseContextTokens)
	}
	result, err := runner.Run(runCtx, agent.Request{
		Messages:     messages,
		UserQuestion: userText,
		Runtime: registry.Runtime{
			UserID:   req.UserID,
			Channel:  req.Channel,
			ThreadTS: req.ThreadTS,
		},
		Locale:   locale,
		RunID:    runID,
		Steering: s.steering(active),
	})
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
			if useStream {
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
		if useStream && !progressStopped {
			appendProgress([]map[string]any{
				{"type": "task_update", "id": taskID, "title": failedTitleWithErrorID(locale, errorID), "status": "error"},
			})
		}
		return true
	}

	sess.Turns = append(sess.Turns, memory.FilterPersistentTurns(memory.FromLLM(result.Generated))...)

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
		if !useStream && (contextUsage != "" || compressed || runObserver != nil) {
			var notices []string
			if runObserver != nil {
				billed := runObserver.BilledTokens()
				billed += priorConversationTokens
				run := runObserver.Run
				noticeParts := []string{formatTokenCount(billed) + " tokens consumed"}
				if run != nil && run.EstimatedCostUSD > 0 {
					noticeParts = append(noticeParts, fmt.Sprintf("~$%.4f", run.EstimatedCostUSD))
				}
				notices = append(notices, strings.Join(noticeParts, " · "))
			}
			if contextUsage != "" {
				notices = append(notices, contextUsage+" context")
			}
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
		// Mark the progress stream as complete when the answer stayed on it.
		if !progressStopped {
			syncCumulativeTokens()
			appendTaskUpdate(agent.CompleteTitle(locale), "complete")
			stopProgress()
		}
		answerStreamOK := result.Streamed && answerStream != nil && !answerStream.Failed() && answerStream.TS() != ""
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
			if answerStream != nil && answerStream.Failed() {
				finalText = "_streaming delivery failed, here is the answer:_\n\n" + finalText
			} else if progressAppendFailed {
				finalText = "_streaming delivery failed, here is the answer:_\n\n" + finalText
			}
			if !useStream && (contextUsage != "" || compressed || runObserver != nil) {
				var notices []string
				// Show cumulative billed tokens and cost (what the provider charges).
				if runObserver != nil {
					billed := runObserver.BilledTokens()
					billed += priorConversationTokens
					run := runObserver.Run
					noticeParts := []string{formatTokenCount(billed) + " tokens consumed"}
					if run != nil && run.EstimatedCostUSD > 0 {
						noticeParts = append(noticeParts, fmt.Sprintf("~$%.4f", run.EstimatedCostUSD))
					}
					notices = append(notices, strings.Join(noticeParts, " · "))
				}
				// Also show context-window occupancy for compaction awareness.
				if contextUsage != "" {
					notices = append(notices, contextUsage+" context")
				}
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

func (s *Service) priorConversationBilledTokens(ctx context.Context, sessionID string, current *runs.Observer) int {
	if s.RunStore == nil || sessionID == "" {
		return 0
	}
	runsList, err := s.RunStore.List(ctx, 10_000)
	if err != nil {
		log.Printf("failed to list prior runs for session=%s: %v", sessionID, err)
		return 0
	}
	currentID := ""
	if current != nil && current.Run != nil {
		currentID = current.Run.ID
	}
	total := 0
	for _, run := range runsList {
		if run.SessionID != sessionID || run.ID == currentID {
			continue
		}
		total += runs.BilledTokens(run.Usage)
	}
	return total
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

func contextTokensFromStreamUsage(usage llm.Usage, baseContextTokens int) int {
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
		return inputTokens + usage.CompletionTokens
	}
	if baseContextTokens > 0 {
		return baseContextTokens + usage.CompletionTokens
	}
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	return usage.CompletionTokens
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
	return formatTokenCount(used) + " tokens (" + strconv.Itoa(percent) + "%)"
}

func titleWithContext(title, contextUsage string) string {
	contextUsage = strings.TrimSpace(contextUsage)
	if contextUsage == "" {
		return title
	}
	return title + "    ·    " + contextUsage
}

// streamingTaskTitle builds the task-update title in the format:
//
//	"xx,xxx tokens · xx% · {status}"
//
// where "xx,xxx tokens" is the cumulative billed count across all LLM calls
// and "xx%" is the current context-window occupancy.  Either or both metric
// parts are omitted when the data is not yet available.
func streamingTaskTitle(status string, cumulTokens, ctxTokens, maxCtxTokens int) string {
	var parts []string
	if cumulTokens > 0 {
		parts = append(parts, formatTokenCount(cumulTokens)+" tokens")
	}
	if ctxTokens > 0 {
		limit := maxCtxTokens
		if limit <= 0 {
			limit = memory.DefaultMaxContextTokens
		}
		pct := ctxTokens * 100 / limit
		if pct == 0 {
			pct = 1
		}
		parts = append(parts, strconv.Itoa(pct)+"%")
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

func formatTokenCount(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	s := strconv.Itoa(n)
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
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
	summary := existing
	compressed := false

	if s.Compactor != nil && len(turns) > 0 {
		llmMessages := memory.ToLLM(turns)
		compacted, result, err := s.Compactor.CompactIfNeeded(ctx, llmMessages)
		if err != nil {
			log.Printf("conversation compact error: %v", err)
		} else if result != nil && result.Layer != "" {
			turns = memory.FilterPersistentTurns(memory.FromLLM(compacted))
			if result.PostTokens < result.PreTokens || result.Summary != "" {
				compressed = true
			}
			if result.Layer == "llm_compact" && strings.TrimSpace(result.Summary) != "" {
				summary = trimSummary(result.Summary, s.Memory.MaxSummaryChars)
			}
		}
	}

	tokenBudget := s.maxTurns * 1000
	llmMessages := memory.ToLLM(turns)
	totalTokens := memory.EstimateTokens(llmMessages)

	if totalTokens <= tokenBudget {
		return turns, summary, compressed
	}

	keep := s.selectImportantTurns(turns, tokenBudget)
	removed := len(turns) - len(keep)
	if removed <= 0 {
		return keep, summary, compressed
	}

	addition := summarizeTurns(turns[:removed])
	if addition == "" {
		return keep, summary, true
	}
	trimmedSummary := strings.TrimSpace(summary)
	if trimmedSummary != "" {
		trimmedSummary += "\n"
	}
	trimmedSummary += addition
	return keep, trimSummary(trimmedSummary, s.Memory.MaxSummaryChars), true
}

// selectImportantTurns picks the most important turns to keep within the token budget.
// It always preserves the most recent turns and high-importance older turns.
func (s *Service) selectImportantTurns(turns []memory.Turn, tokenBudget int) []memory.Turn {
	if len(turns) == 0 {
		return nil
	}

	// Always keep recent turns (last 40% of the list, minimum 4).
	recentCount := len(turns) * 40 / 100
	if recentCount < 4 {
		recentCount = 4
	}
	if recentCount > len(turns) {
		recentCount = len(turns)
	}
	recentFrom := len(turns) - recentCount

	// Score older turns by importance.
	type scoredTurn struct {
		index int
		score int
	}
	var scored []scoredTurn
	for i := 0; i < recentFrom; i++ {
		scored = append(scored, scoredTurn{
			index: i,
			score: scoreTurnImportance(&turns[i]),
		})
	}

	// Sort by score descending (insertion sort for simplicity).
	for i := 1; i < len(scored); i++ {
		for j := i; j > 0 && scored[j].score > scored[j-1].score; j-- {
			scored[j], scored[j-1] = scored[j-1], scored[j]
		}
	}

	// Build kept set: recent turns + high-scoring older turns within budget.
	keepSet := make(map[int]bool)
	currentTokens := 0

	// First add recent turns.
	for i := recentFrom; i < len(turns); i++ {
		tokens := memory.RoughTokenEstimate(turns[i].Content)
		currentTokens += tokens
		keepSet[i] = true
	}

	// Then fill remaining budget with important older turns.
	for _, st := range scored {
		tokens := memory.RoughTokenEstimate(turns[st.index].Content)
		if currentTokens+tokens > tokenBudget {
			continue
		}
		keepSet[st.index] = true
		currentTokens += tokens
	}

	// Build result in order, never breaking tool_use/tool_result pairs.
	result := make([]memory.Turn, 0, len(keepSet))
	for i := 0; i < len(turns); i++ {
		if keepSet[i] {
			// Skip orphaned tool results at the start.
			if turns[i].Role == memory.RoleTool && len(result) == 0 {
				continue
			}
			result = append(result, turns[i])
		}
	}
	return result
}

// scoreTurnImportance rates a turn's importance for context retention.
func scoreTurnImportance(turn *memory.Turn) int {
	score := 0
	content := strings.ToLower(turn.Content)

	// User messages are always high priority.
	if turn.Role == memory.RoleUser {
		score += 10
	}

	// Error/failure signals.
	for _, keyword := range []string{
		"error", "exception", "failed", "failure", "timeout",
	} {
		if strings.Contains(content, keyword) {
			score += 5
		}
	}

	// Decision signals.
	for _, keyword := range []string{
		"decision", "decided", "should", "must", "important",
	} {
		if strings.Contains(content, keyword) {
			score += 3
		}
	}

	// Tool results with substantial content.
	if turn.Role == memory.RoleTool && len(turn.Content) > 100 {
		score += 2
	}

	// Assistant messages with tool calls.
	if turn.Role == memory.RoleAssistant && len(turn.ToolCalls) > 0 {
		score += 3
	}

	return score
}

// summarizeTurns generates a structured summary of removed turns.
// It preserves key information: user intents, tool call names, error states,
// and content excerpts — far more useful than the old flat truncation.
func summarizeTurns(turns []memory.Turn) string {
	if len(turns) == 0 {
		return ""
	}

	var userMessages []string
	var toolCalls []string
	var errors []string
	var assistantExcerpts []string

	for _, turn := range turns {
		content := strings.Join(strings.Fields(strings.TrimSpace(turn.Content)), " ")

		switch turn.Role {
		case memory.RoleUser:
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			if content != "" {
				userMessages = append(userMessages, content)
			}

		case memory.RoleAssistant:
			if len(turn.ToolCalls) > 0 {
				names := make([]string, 0, len(turn.ToolCalls))
				for _, call := range turn.ToolCalls {
					names = append(names, call.Name)
				}
				toolCalls = append(toolCalls, strings.Join(names, ", "))
			} else if content != "" {
				if len(content) > 240 {
					content = content[:240] + "..."
				}
				assistantExcerpts = append(assistantExcerpts, content)
			}

		case memory.RoleTool:
			if strings.HasPrefix(content, "[tool error]") {
				if len(content) > 200 {
					content = content[:200] + "..."
				}
				errors = append(errors, turn.Name+": "+content)
			}
		}
	}

	lines := make([]string, 0, 8)
	lines = append(lines, "Trimmed conversation summary:")

	if len(userMessages) > 0 {
		lines = append(lines, "User messages:")
		for _, m := range userMessages {
			lines = append(lines, "  - "+m)
		}
	}
	if len(toolCalls) > 0 {
		lines = append(lines, "Tools called: "+strings.Join(toolCalls, "; "))
	}
	if len(errors) > 0 {
		lines = append(lines, "Errors encountered:")
		for _, e := range errors {
			lines = append(lines, "  - "+e)
		}
	}
	if len(assistantExcerpts) > 0 {
		lines = append(lines, "Assistant responses:")
		for _, e := range assistantExcerpts {
			lines = append(lines, "  - "+e)
		}
	}

	return strings.Join(lines, "\n")
}

// trimSummary trims a summary to fit within the character budget.
// It preserves the tail (most recent additions) and uses head+tail
// truncation rather than pure tail truncation.
func trimSummary(summary string, max int) string {
	summary = strings.TrimSpace(summary)
	runes := []rune(summary)
	if max <= 0 || len(runes) <= max {
		return summary
	}
	// Keep the most recent portion (tail), with a marker.
	marker := "\n...[older summary truncated]...\n"
	markerRunes := len([]rune(marker))
	if max <= markerRunes+100 {
		return strings.TrimSpace(string(runes[len(runes)-max:]))
	}
	tailSize := max - markerRunes
	return marker + strings.TrimSpace(string(runes[len(runes)-tailSize:]))
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

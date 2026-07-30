package conversation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/memory"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	"github.com/noknov/slack-copilot-agent/packages/runs"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/session"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
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
	ToolSpillStore registry.ToolSpillStore
	RunProvider    string
	RunModel       string
	ModelRouter    ModelRouter
	Multimodal     func(model string) bool
	// WebSearchEnabled controls whether the web-search tool is available for
	// a given user. When it returns false the tool is excluded from the LLM
	// tool list for that request. When nil, search is always available.
	WebSearchEnabled func(userID string) bool
	CostRates        observability.CostRates
	HealthSummary    func() string
	AutoTTS          AutoTTSFunc
	TTSSummarizer    *TTSSummarizer
	Redis            *redisclient.Client
	// PodID identifies this process instance for cross-pod active run routing.
	PodID string
	// RunSemaphore bounds expensive LLM/tool executions across ingress paths.
	// It is shared by the Slack and Web services created by app.Server.
	RunSemaphore chan struct{}
	// MaxConcurrentRuns is the cluster-wide limit enforced via Redis.
	MaxConcurrentRuns int
	// FollowUpContext is the service lifecycle context used for queued
	// follow-up turns. It lets shutdown cancel work that was spawned after the
	// original Slack request context had already ended.
	FollowUpContext context.Context

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	// active is intentionally separate from per-session locks: in-flight
	// replies must be queued/cancelled without blocking on the running turn.
	active map[string]*activeRun
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
		active:         map[string]*activeRun{},
	}
}

func (s *Service) HandleMention(ctx context.Context, req Request) bool {
	if s.controlActive(req) {
		return true
	}
	return s.process(ctx, req, false)
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
	releaseRunSlot, ok := s.acquireRunSlot(ctx)
	if !ok {
		return false
	}
	defer releaseRunSlot()
	sessionID := session.ID(req.Channel, req.ThreadTS)
	var unlock func()
	if locker, ok := s.Store.(session.Locker); ok {
		var err error
		unlock, err = locker.Lock(ctx, sessionID)
		if err != nil {
			s.reportError(ctx, req, "Failed to lock session: "+s.Redactor.Sanitize(err.Error()))
			return false
		}
	} else {
		lock := s.lockFor(sessionID)
		lock.Lock()
		unlock = lock.Unlock
	}
	var followUps []Request
	defer func() {
		unlock()
		if followUp, ok := combineQueuedFollowUps(followUps); ok {
			baseCtx := s.FollowUpContext
			if baseCtx == nil {
				baseCtx = context.Background()
			}
			// Use a lifecycle-bound context with a generous timeout instead of
			// inheriting the original Slack event context, which may already be
			// cancelled after the first turn has been acknowledged.
			followCtx, followCancel := context.WithTimeout(baseCtx, 30*time.Minute)
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
	route := s.routeModel(req)
	runObserver := s.newRunObserver(sessionID, req, start, route)
	runID := ""
	if runObserver != nil && runObserver.Run != nil {
		runID = runObserver.Run.ID
	}

	// Use session-stored locale for consistency across the thread.
	// Only detect from text on the first message.
	locale := sess.Locale
	if locale == "" {
		locale = agent.DetectLocale(req.Text)
		sess.Locale = locale
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	active := newActiveRun(sessionID, req.UserID, cancelRun)
	s.registerActive(sessionID, active)
	progress := newTurnProgress(ctx, runCtx, s, req, locale)
	active.setProgress(locale, progress.ProgressAppender())
	defer func() {
		if s.Metrics != nil {
			s.Metrics.Latency(time.Since(start))
		}
		progress.cleanup()
	}()
	defer func() {
		s.unregisterActive(sessionID, active)
		followUps = append(followUps, active.remainingQueued()...)
		cancelRun()
	}()

	runner := s.Runner
	runner.Tools = runner.Tools.Clone()
	if route.Model != "" {
		runner.Model = route.Model
	}
	if runObserver != nil {
		runner.Observer = multiObserver{s.Metrics, runObserver}
	}
	progress.WireRunner(&runner)

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

	threadContext := s.cachedThreadContext(ctx, req.Channel, req.ThreadTS)
	activeMemory := s.pipeline().BuildActiveRequest(memory.ActiveRequestInput{
		SystemPrompt:      sysPrompt,
		CompactBoundaries: sess.CompactBoundaries,
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
	progress.SetBaseContextTokens(activeMemory.EstimatedTokens)
	agentReq := agent.Request{
		Messages:     messages,
		UserQuestion: userText,
		Runtime: registry.Runtime{
			UserID:         req.UserID,
			Channel:        req.Channel,
			ThreadTS:       req.ThreadTS,
			ToolSpillStore: s.ToolSpillStore,
		},
		Locale:                  locale,
		RunID:                   runID,
		Steering:                s.steering(active),
		ContentReplacementState: contentReplacementState,
		MemoryBreakdown:         activeMemory.TokenBreakdown,
	}
	if webSearchOff {
		agentReq.DisabledTools = []string{"web-search", "web-read_page"}
	}
	result, err := runner.Run(runCtx, agentReq)
	evidenceText := webEvidenceMarkdown(result.Generated, locale)
	if progress.AnswerStream() != nil && evidenceText != "" {
		progress.AnswerStream().Write(evidenceText)
	}
	progress.CloseAnswerStream()
	contextUsageTokens := contextUsageTokenCount(messages, result.Generated)
	contextUsage := contextUsageText(s.Memory.MaxContextTokens, contextUsageTokens)
	progress.SetContextUsage(contextUsageTokens)

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
			if progress.UseNativeStatus() {
				// In native status mode the stream task_update path uses runCtx which is
				// already canceled by the defer; post a real message so the user sees the
				// cancel confirmation. The post itself clears the setStatus indicator.
				s.reportError(ctx, req, interruptedMessage(locale))
			} else if progress.UseStream() {
				progress.CloseMarkdown()
				progress.AppendTaskUpdate(agent.CancelledTitle(locale), "complete")
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
		if progress.UseNativeStatus() {
			// In native status mode appendProgress uses runCtx (already
			// canceled by defer). Post a real message so the user receives the error ID.
			// The post also clears the setStatus indicator automatically.
			s.reportError(ctx, req, failedMessageWithErrorID(locale, errorID))
		} else if progress.UseStream() && !progress.Stopped() {
			progress.AppendTaskUpdate(failedTitleWithErrorID(locale, errorID), "error")
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
	var compactResult memory.SessionCompactResult
	sess.Turns, sess.Summary, compressed, compactResult = s.trimAndSummarizeWithResult(ctx, sess.Turns, sess.Summary)
	if compressed && compactResult.Layer != "" {
		sess.CompactBoundaries = appendCompactBoundary(sess.CompactBoundaries, memory.NewCompactBoundary(compactResult.Layer, compactResult.Summary, compactResult.PreTokens, compactResult.PostTokens))
	}
	if err := s.Store.Save(ctx, sess); err != nil && s.Metrics != nil {
		s.Metrics.Error(err)
	}
	if progress.UseStream() && compressed && !progress.Stopped() {
		progress.AppendTaskUpdate(contextCompressingTitle(locale), "in_progress")
	}
	if result.Pending && result.PendingQuestion != "" {
		pendingText := s.Redactor.Sanitize(result.PendingQuestion)
		if s.Format != nil {
			pendingText = s.Format(pendingText)
		}
		if progress.UseStream() {
			progress.AppendTaskUpdate(agent.WaitingTitle(locale), "complete")
			progress.Stop()
		}
		if !progress.UseStream() && contextUsage != "" {
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
		answerStreamOK := progress.AnswerStreamOK(result.Streamed)
		// In native Slack status mode, keep the status visible until the
		// answer stream is finalized so the UI does not briefly show an empty
		// handoff between thinking and output.
		if !progress.Stopped() && !(progress.UseNativeStatus() && answerStreamOK) {
			progress.AppendTaskUpdate(agent.CompleteTitle(locale), "complete")
			progress.Stop()
		}
		if answerStreamOK {
			_ = s.Messenger.StopStream(ctx, req.Channel, progress.AnswerStream().TS())
			if runObserver != nil {
				runObserver.LinkSlackMessage(req.Channel, progress.AnswerStream().TS())
			}
			s.maybeAutoTTS(req.Channel, req.ThreadTS, finalText)
		} else {
			if s.Format != nil {
				finalText = s.Format(finalText)
			}
			if !progress.UseStream() && contextUsage != "" {
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

func (s *Service) routeModel(req Request) ModelRoute {
	route := s.ModelRouter.Route(req)
	if route.Model != "" {
		return route
	}
	model := s.RunModel
	if model == "" {
		model = s.Runner.Model
	}
	return ModelRoute{Model: model, Reason: "default"}
}

func (s *Service) newRunObserver(sessionID string, req Request, startedAt time.Time, route ModelRoute) *runs.Observer {
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
		Model:     route.Model,
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

const (
	eventSeenTTL     = 10 * time.Minute
	threadContextTTL = 30 * time.Second
)

func (s *Service) cachedThreadContext(ctx context.Context, channel, threadTS string) string {
	if channel == "" || threadTS == "" {
		return ""
	}
	key := "slack:thread:" + channel + ":" + threadTS
	if s.Redis != nil {
		if cached, err := s.Redis.Get(ctx, key); err == nil && cached != "" {
			return cached
		}
	}
	result := s.Messenger.ThreadContext(ctx, channel, threadTS, 0)
	if s.Redis != nil && result != "" {
		_ = s.Redis.Set(ctx, key, result, threadContextTTL)
	}
	return result
}

func (s *Service) markEvent(eventID string) bool {
	if eventID == "" {
		return true
	}
	if s.Redis == nil {
		return true
	}
	ok, err := s.Redis.SetNX(context.Background(), "event:seen:"+eventID, "1", eventSeenTTL)
	if err != nil {
		log.Printf("markEvent redis SetNX: %v (falling through)", err)
		return true
	}
	return ok
}

const activeRunTTL = 30 * time.Minute

func (s *Service) registerActive(sessionID string, run *activeRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		s.active = map[string]*activeRun{}
	}
	s.active[sessionID] = run
	if s.Redis != nil {
		_ = s.Redis.Set(context.Background(), "active:"+sessionID, s.PodID, activeRunTTL)
	}
}

func (s *Service) unregisterActive(sessionID string, run *activeRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[sessionID] == run {
		delete(s.active, sessionID)
	}
	if s.Redis != nil {
		_ = s.Redis.Del(context.Background(), "active:"+sessionID)
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
	sessionID := session.ID(req.Channel, req.ThreadTS)
	activeKey := "active:" + sessionID

	if local := s.activeFor(sessionID); local != nil && local.userID == req.UserID {
		if !s.markEvent(req.EventID) {
			return true
		}
		if isCancelRequest(req.Text) {
			local.interrupt()
			return true
		}
		local.enqueue(req)
		return true
	}

	if s.Redis == nil {
		return false
	}
	ownerPod, err := s.Redis.Get(context.Background(), activeKey)
	if err != nil || ownerPod == "" || ownerPod == s.PodID {
		return false
	}
	if !s.markEvent(req.EventID) {
		return true
	}
	action := "steer"
	if isCancelRequest(req.Text) {
		action = "cancel"
	}
	payload, _ := json.Marshal(map[string]string{
		"session": sessionID,
		"action":  action,
		"user":    req.UserID,
		"event":   req.EventID,
		"text":    req.Text,
		"channel": req.Channel,
		"thread":  req.ThreadTS,
	})
	subscribers, err := s.Redis.PublishCount(context.Background(), "pod:control:"+ownerPod, string(payload))
	if err != nil {
		s.unmarkEvent(req.EventID)
		log.Printf("active run control publish failed session=%s owner=%s err=%v", sessionID, ownerPod, err)
		return false
	}
	if subscribers == 0 {
		s.unmarkEvent(req.EventID)
		_ = s.Redis.Del(context.Background(), activeKey)
		log.Printf("active run owner unavailable session=%s owner=%s; handling event locally", sessionID, ownerPod)
		return false
	}
	return true
}

func (s *Service) unmarkEvent(eventID string) {
	if eventID == "" || s.Redis == nil {
		return
	}
	_ = s.Redis.Del(context.Background(), "event:seen:"+eventID)
}

// StartControlSubscriber listens for cross-pod cancel/steer commands.
func (s *Service) StartControlSubscriber(ctx context.Context) {
	if s.PodID == "" || s.Redis == nil {
		return
	}
	sub := s.Redis.Subscribe(ctx, "pod:control:"+s.PodID)
	defer sub.Close()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var cmd struct {
				Session string `json:"session"`
				Action  string `json:"action"`
				User    string `json:"user"`
				Text    string `json:"text"`
			}
			if json.Unmarshal([]byte(msg.Payload), &cmd) != nil {
				continue
			}
			active := s.activeFor(cmd.Session)
			if active == nil || active.userID != cmd.User {
				continue
			}
			switch cmd.Action {
			case "cancel":
				active.interrupt()
			case "steer":
				active.enqueue(Request{UserID: cmd.User, Text: cmd.Text})
			}
		}
	}
}

func (s *Service) trimAndSummarize(ctx context.Context, turns []memory.Turn, existing string) ([]memory.Turn, string, bool) {
	result := s.pipeline().CompactSessionConversation(ctx, turns, existing)
	return result.Turns, result.Summary, result.Compressed
}

func (s *Service) trimAndSummarizeWithResult(ctx context.Context, turns []memory.Turn, existing string) ([]memory.Turn, string, bool, memory.SessionCompactResult) {
	result := s.pipeline().CompactSessionConversation(ctx, turns, existing)
	return result.Turns, result.Summary, result.Compressed, result
}

func (s *Service) pipeline() *memory.Pipeline {
	if s.MemoryPipeline != nil {
		return s.MemoryPipeline
	}
	return memory.NewPipeline(s.Memory, s.Compactor)
}

const redisRunSemKey = "agent:runs:active"

func (s *Service) acquireRunSlot(ctx context.Context) (func(), bool) {
	localAcquired := false
	redisAcquired := false
	if s.RunSemaphore != nil {
		select {
		case s.RunSemaphore <- struct{}{}:
			localAcquired = true
		case <-ctx.Done():
			return nil, false
		}
	}
	if s.Redis == nil {
		return func() { s.releaseRunSlot(localAcquired, false) }, true
	}
	limit := s.MaxConcurrentRuns
	if limit <= 0 {
		limit = 16
	}
	for {
		n, err := s.Redis.Incr(ctx, redisRunSemKey)
		if err != nil {
			return func() { s.releaseRunSlot(localAcquired, false) }, true
		}
		if n <= int64(limit) {
			_ = s.Redis.Expire(ctx, redisRunSemKey, 10*time.Minute)
			redisAcquired = true
			return func() { s.releaseRunSlot(localAcquired, redisAcquired) }, true
		}
		_, _ = s.Redis.Decr(ctx, redisRunSemKey)
		select {
		case <-ctx.Done():
			s.releaseRunSlot(localAcquired, false)
			return nil, false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *Service) releaseRunSlot(localAcquired, redisAcquired bool) {
	if redisAcquired && s.Redis != nil {
		_, _ = s.Redis.Decr(context.Background(), redisRunSemKey)
	}
	if localAcquired && s.RunSemaphore != nil {
		<-s.RunSemaphore
	}
}

func newErrorID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "err-" + time.Now().UTC().Format("20060102150405")
	}
	return "err-" + hex.EncodeToString(b[:])
}

func appendCompactBoundary(boundaries []memory.CompactBoundary, boundary memory.CompactBoundary) []memory.CompactBoundary {
	if boundary.Layer == "" {
		return boundaries
	}
	const maxBoundaries = 20
	boundaries = append(boundaries, boundary)
	if len(boundaries) > maxBoundaries {
		return append([]memory.CompactBoundary(nil), boundaries[len(boundaries)-maxBoundaries:]...)
	}
	return boundaries
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

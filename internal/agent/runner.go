package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/memory"
	"github.com/wati/oncall-agent/internal/prompts"
	"github.com/wati/oncall-agent/internal/runs"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

var (
	ErrRepetitiveOutput = errors.New("model output repeated itself")
	ErrTextualToolCall  = errors.New("model returned textual tool invocation instead of structured tool calls")
	ErrMaxToolSteps     = errors.New("agent exceeded max tool steps")
	ErrEmptyFinal       = errors.New("model returned empty final response")
)

const (
	// Only trigger clarification after many repeated failures — give the model
	// ample opportunity to self-correct first. Most tool errors (wrong path,
	// wrong branch, wrong repo) are technical mistakes the model can fix itself.
	clarificationErrorThreshold = 4

	// Progressive escalation thresholds. Pressure increases at each tier
	// to prevent aimless searching while still allowing genuine investigations.
	maxOutputTokensRecoveryLimit = 3

	// maxIdenticalFailedCallAttempts is the number of identical failed tool
	// calls allowed before the circuit breaker short-circuits a subsequent
	// identical call. Successful duplicate calls are legitimate for rereads,
	// polling, or state checks, so only failures count.
	maxIdenticalFailedCallAttempts = 3

	// maxIdenticalSuccessCallAttempts is the number of times the same
	// successful tool call (same name + args) may repeat before the circuit
	// breaker injects a warning. A subsequent identical call is blocked. This
	// catches loops where the model navigates to the same URL, re-reads the same
	// file, or polls the same endpoint repeatedly without making progress.
	maxIdenticalSuccessCallAttempts = 5
)

type Observer interface {
	LLMCall(usage llm.Usage, d time.Duration, err error)
	ToolCall(name string, d time.Duration, err error)
}

type MetadataObserver interface {
	ToolCallWithMetadata(name string, args json.RawMessage, d time.Duration, err error)
}

type LLMResponseObserver interface {
	LLMResponse(resp llm.Response, d time.Duration, err error)
}

type EventObserver interface {
	Event(name string, metadata map[string]any)
}

type StatusUpdater func(status string)

type ObservationFormatter interface {
	ToolObservation(toolName string, output string) string
}

type Sanitizer interface {
	Sanitize(text string) string
}

type SteeringProvider func() []llm.Message

type Runner struct {
	LLM          llm.Client
	Model        string
	Thinking     string
	MaxTokens    int
	Temp         float64
	Tools        *registry.Registry
	Capabilities llm.Capabilities
	Format       ObservationFormatter
	Sanitize     Sanitizer
	Observer     Observer
	MaxSteps     int
	Compactor    *memory.Compactor
	StatusUpdate StatusUpdater
	// LoadingMessageUpdate receives LLM-generated progress summaries for
	// native loading_messages; StatusUpdate remains the coarse static status.
	LoadingMessageUpdate StatusUpdater
	StatusSummarizer     *StatusSummarizer
	OnStream             func(StreamEvent)
	OnUsage              func(llm.Usage)
	OnLLMStepComplete    func()
}

type Request struct {
	Messages                []llm.Message
	UserQuestion            string
	Runtime                 registry.Runtime
	Locale                  string
	RunID                   string
	Steering                SteeringProvider
	ContentReplacementState *memory.ContentReplacementState
	// DisabledTools lists tool names to exclude from this run's tool spec
	// list. The tools remain registered; they are simply not offered to the
	// model for this specific request.
	DisabledTools []string
}

// TerminationReason describes why the agent loop exited. It is analogous to
// claude-code's Terminal.reason enum and is used for observability and testing.
type TerminationReason string

const (
	TerminationCompleted   TerminationReason = "completed"    // normal text answer
	TerminationPending     TerminationReason = "pending_user" // waiting for user input
	TerminationMaxSteps    TerminationReason = "max_steps"    // hit step budget
	TerminationCanceled    TerminationReason = "canceled"     // context canceled
	TerminationModelError  TerminationReason = "model_error"  // unrecoverable LLM error
	TerminationEmptyFinal  TerminationReason = "empty_final"  // persistent empty response
	TerminationRepetitive  TerminationReason = "repetitive"   // model looping
	TerminationTextualTool TerminationReason = "textual_tool" // model hallucinating tool calls
)

type Result struct {
	Generated         []llm.Message
	Final             string
	Pending           bool
	PendingQuestion   string
	Streamed          bool
	TerminationReason TerminationReason
}

// loopState holds all mutable per-run state that was previously scattered as
// local boolean variables in the agent loop. Grouping them here mirrors
// claude-code's explicit State struct and makes the loop logic easier to follow.
type loopState struct {
	messages  []llm.Message
	generated []llm.Message

	// retried is a one-shot guard set per check name. Once set, the next
	// failure of that check becomes a hard error instead of triggering another
	// retry. Adding a new check never requires touching this struct.
	retried map[string]bool

	// overload / max-output-tokens recovery counters
	overloadRetries              int
	maxOutputTokensRecoveryCount int

	// tracking for code-evidence validation
	codeToolCalledThisRun bool

	// afterTools is true once at least one tool result has been received.
	afterTools bool

	// answerFlushed is true once any StreamAnswer event has been emitted.
	// Subsequent retry steps must not write more text to the answer stream.
	answerFlushed bool

	// streamedText tracks whether any text was emitted in the current step.
	streamedText bool

	// toolStatusStarted is set once the dynamic status summarizer has been
	// launched for the current tool-call turn.
	toolStatusStarted bool

	// lastStatusSummaryAt throttles secondary-model status summaries.
	lastStatusSummaryAt time.Time

	// clarificationErrors tracks persistent tool failures that require user input.
	clarificationErrors *clarificationErrorTracker

	// identicalCallFailures is the circuit breaker for identical failed tool
	// calls. Keyed by "toolName::sha256(args)" → consecutive failure count.
	identicalCallFailures map[string]int

	// identicalCallSuccesses tracks how many times each identical tool call
	// has succeeded consecutively. Used to detect progress-less loops where
	// the model keeps calling the same tool with the same args successfully
	// (e.g. navigating to the same URL repeatedly) without advancing.
	identicalCallSuccesses map[string]int

	// per-step streaming components; reset each iteration by callLLM.
	streamRouter *streamRouter
	streamExec   *streamingToolExecutor
}

// retryOnce returns true the first time key is seen, false thereafter.
// Used to give each validation check exactly one retry attempt.
func (s *loopState) retryOnce(key string) bool {
	if s.retried == nil {
		s.retried = make(map[string]bool)
	}
	if s.retried[key] {
		return false
	}
	s.retried[key] = true
	return true
}

func repetitiveRetryPrompt() string {
	return prompts.RunnerPrompt("repetitive_retry", "")
}

func textualToolCallRetryPrompt() string {
	return prompts.RunnerPrompt("textual_tool_call_retry", "")
}

func emptyResponseRetryPrompt() string {
	return prompts.RunnerPrompt("empty_response_retry", "")
}

func maxOutputTokensRecoveryPrompt() string {
	return prompts.RunnerPrompt("max_output_tokens_recovery", "Output token limit hit. Continue from the partial answer above and finish more concisely. Avoid repeating prior text; prefer a short direct conclusion unless more evidence is genuinely required.")
}

func codeClaimRetryPrompt() string {
	return prompts.RunnerPrompt("code_claim_retry", "")
}

func rawEvidenceRetryPrompt() string {
	return prompts.RunnerPrompt("raw_evidence_retry", "")
}

// codeReadingTools is the set of tool names that constitute evidence for code
// claims. A final response containing a fenced code block should be preceded
// by at least one call to one of these tools in the same run.
var codeReadingTools = map[string]bool{
	"git-search_ref":    true,
	"git-read_file_ref": true,
	"repo-search":       true,
	"repo-read_file":    true,
	"code-search":       true,
	"code-read_file":    true,
	"code-symbols":      true,
	"code-definition":   true,
	"code-references":   true,
	"explore-code":      true,
}

// hasFencedCodeBlock reports whether text contains a fenced code block
// (a line beginning with three or more backticks, optionally indented).
func hasFencedCodeBlock(text string) bool {
	for len(text) > 0 {
		end := strings.IndexByte(text, '\n')
		line := text
		if end >= 0 {
			line = text[:end]
			text = text[end+1:]
		} else {
			text = ""
		}
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "```") {
			return true
		}
	}
	return false
}

var fileLineRefPattern = regexp.MustCompile(`\w+\.\w+:\d+`)

// hasUnverifiedCodeClaim reports whether text contains a fenced code block
// or a file:line reference (e.g. "handler.go:45") that would need code tool
// evidence to substantiate.
func hasUnverifiedCodeClaim(text string) bool {
	return hasFencedCodeBlock(text) || fileLineRefPattern.MatchString(text)
}

// filePathPattern matches path-like references in prose, e.g.
// "connectionService.go:182", "controller/v2/handler.go", "settingRepository.go:173".
var filePathPattern = regexp.MustCompile(`(?:[\w/.-]+/)?(\w+\.(?:go|ts|tsx|js|jsx|cs|py|java|rb|rs|yml|yaml|json))\b`)

// extractReferencedFiles returns base filenames referenced in the answer text.
func extractReferencedFiles(text string) map[string]struct{} {
	matches := filePathPattern.FindAllStringSubmatch(text, -1)
	files := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		files[m[1]] = struct{}{}
	}
	return files
}

// extractFilesFromToolResults returns base filenames that appeared in code
// tool results (search hits, file reads).
func extractFilesFromToolResults(messages []llm.Message) map[string]struct{} {
	files := make(map[string]struct{})
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, m := range filePathPattern.FindAllStringSubmatch(msg.Content, -1) {
			files[m[1]] = struct{}{}
		}
	}
	return files
}

// hasUnevidencedFileReference checks whether the answer references code files
// that never appeared in any tool result from this run. This catches the case
// where the model read file A but makes claims about file B it never read.
func hasUnevidencedFileReference(answerText string, toolMessages []llm.Message) bool {
	referenced := extractReferencedFiles(answerText)
	if len(referenced) == 0 {
		return false
	}
	evidenced := extractFilesFromToolResults(toolMessages)
	for f := range referenced {
		if _, ok := evidenced[f]; !ok {
			return true
		}
	}
	return false
}

func unevidencedFileRetryPrompt() string {
	return prompts.RunnerPrompt("unevidenced_file_retry", "Your answer references code files that do not appear in any tool result from this run. Before answering, read the specific file(s) you are making claims about. Do not infer behavior from files you have not read — the same function name or pattern can behave differently in different files or code paths.")
}

func (r Runner) Run(ctx context.Context, req Request) (Result, error) {
	maxSteps := r.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 120 // hard ceiling; complex agentic tasks may need 60–100 steps
	}
	if req.Runtime.Cache == nil {
		req.Runtime.Cache = registry.NewRuntimeCache()
	}
	if strings.TrimSpace(req.RunID) == "" {
		req.RunID = runs.NewID()
	}
	req.Runtime.RunID = req.RunID

	// Initialise loop state. Mirrors claude-code's explicit State struct so
	// the loop body only reads/writes s.* rather than scattered local vars.
	s := &loopState{
		messages:            append([]llm.Message(nil), req.Messages...),
		clarificationErrors: newClarificationErrorTracker(),
	}
	if hint := localeHint(req.Locale); hint != "" {
		s.messages = append(s.messages, llm.Message{Role: "system", Content: hint})
	}

	const maxOverloadRetries = 3

	for step := 0; step < maxSteps; step++ {
		if done, result, err := r.runStep(ctx, step, maxOverloadRetries, s, req); done {
			return result, err
		}
	}

	// Max steps reached: one final tool-free LLM call to force a summary answer.
	return r.handleMaxSteps(ctx, s, req)
}

// runStep executes one iteration of the agent loop (one LLM call + tool round).
// Returns (true, result, err) when the loop should terminate, (false, _, _) to continue.
func (r Runner) runStep(ctx context.Context, step, maxOverloadRetries int, s *loopState, req Request) (done bool, result Result, err error) {
	// Bail early on context cancellation so we return a clean error rather
	// than letting the LLM call fail with a confusing wrapped error.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return true, Result{Generated: s.generated, TerminationReason: TerminationCanceled}, ctxErr
	}

	r.emitStepStatus(req.Locale, step, s)

	if req.Steering != nil {
		if steering := req.Steering(); len(steering) > 0 {
			s.messages = append(s.messages, steering...)
		}
	}

	var toolSpecs []llm.ToolSpec
	if r.Tools != nil {
		toolSpecs = r.Tools.Specs()
		if len(req.DisabledTools) > 0 {
			disabled := make(map[string]bool, len(req.DisabledTools))
			for _, name := range req.DisabledTools {
				disabled[name] = true
			}
			filtered := toolSpecs[:0:0]
			for _, spec := range toolSpecs {
				if !disabled[spec.Function.Name] {
					filtered = append(filtered, spec)
				}
			}
			toolSpecs = filtered
		}
	}
	s.messages = r.prepareMessagesForQuery(ctx, s.messages, req, toolSpecs)

	resp, didStream, llmErr := r.callLLM(ctx, llm.Request{
		Model:       r.Model,
		Messages:    s.messages,
		Tools:       toolSpecs,
		MaxTokens:   r.MaxTokens,
		Temperature: r.Temp,
		Thinking:    r.Thinking,
	}, s, req)
	if r.OnLLMStepComplete != nil {
		r.OnLLMStepComplete()
	}
	if llmErr != nil {
		action, cont := r.handleLLMError(ctx, llmErr, maxOverloadRetries, s, req)
		if cont {
			if action == llmErrorOverflowRetry {
				s.messages = r.prepareMessagesForOverflowRetry(ctx, s.messages, req)
			}
			return false, Result{}, nil // continue loop
		}
		return true, Result{Generated: s.generated, TerminationReason: TerminationModelError}, llmErr
	}
	assistantMsg := resp.Message
	if resp.Usage.PromptTokens > 0 || resp.Usage.CompletionTokens > 0 {
		assistantMsg.Usage = &resp.Usage
	}
	assistantMsg.ToolCalls = memory.NormalizeToolCalls(assistantMsg.ToolCalls)

	if s.streamRouter != nil {
		s.streamRouter.finish(len(assistantMsg.ToolCalls) > 0)
		// Note: do NOT propagate answerFlushed here. The streamRouter stores
		// the answer as pendingAnswer; s.answerFlushed is set only after
		// commitAnswer() emits it once handleFinalResponse validation passes.
	}
	if didStream && !s.streamedText && strings.TrimSpace(assistantMsg.Content) != "" {
		didStream = false
	}

	if len(assistantMsg.ToolCalls) == 0 {
		return r.settleFinalResponse(ctx, resp, assistantMsg, didStream, s, req)
	}

	// Tool-call turn: emit narration, execute tools, collect results.
	r.emitNarration(assistantMsg, s)
	r.startToolStatus(ctx, assistantMsg.ToolCalls, s, req)
	assistantMsg.Content = ""
	s.messages = append(s.messages, assistantMsg)
	s.generated = append(s.generated, assistantMsg)

	toolResults := r.runToolCalls(ctx, assistantMsg.ToolCalls, s, req)
	for _, tr := range toolResults {
		s.messages = append(s.messages, tr.message)
		s.generated = append(s.generated, tr.message)
		if codeReadingTools[tr.name] {
			s.codeToolCalledThisRun = true
		}
		if tr.waitForUser {
			return true, Result{
				Generated:         s.generated,
				Pending:           true,
				PendingQuestion:   tr.message.Content,
				TerminationReason: TerminationPending,
			}, nil
		}
	}
	if len(toolResults) > 0 {
		s.afterTools = true
	}
	if question := s.clarificationErrors.UserPrompt(toolResults, requestLocale(req.Locale)); question != "" {
		s.generated = append(s.generated, llm.Message{Role: "assistant", Content: question})
		return true, Result{
			Generated:         s.generated,
			Pending:           true,
			PendingQuestion:   question,
			TerminationReason: TerminationPending,
		}, nil
	}
	return false, Result{}, nil // continue loop
}

// settleFinalResponse handles a no-tool-call LLM response; delegates to
// handleFinalResponse and maps its three-value return into the runStep contract.
func (r Runner) settleFinalResponse(ctx context.Context, resp llm.Response, assistantMsg llm.Message, useStream bool, s *loopState, req Request) (done bool, result Result, err error) {
	ok, cont, finalErr := r.handleFinalResponse(ctx, resp, &assistantMsg, useStream, s, req)
	if cont {
		// Validation failed; discard any buffered pending answer so it is not
		// emitted on subsequent retries. The next LLM call will buffer a fresh one.
		if s.streamRouter != nil {
			s.streamRouter.discardAnswer()
		}
		return false, Result{}, nil // retry within loop
	}
	if finalErr != nil {
		reason := TerminationRepetitive
		switch {
		case errors.Is(finalErr, ErrEmptyFinal):
			reason = TerminationEmptyFinal
		case errors.Is(finalErr, ErrTextualToolCall):
			reason = TerminationTextualTool
		}
		return true, Result{Generated: s.generated, TerminationReason: reason}, finalErr
	}
	if ok {
		// Validation passed — now it is safe to flush the deferred answer
		// to the output stream. This ensures users never see intermediate
		// answers that the agent later rejects and retries.
		if useStream && s.streamRouter != nil {
			s.streamRouter.commitAnswer(assistantMsg.Content)
			if s.streamRouter.answerFlushed {
				s.answerFlushed = true
			}
		}
		return true, Result{
			Generated:         s.generated,
			Final:             assistantMsg.Content,
			Streamed:          useStream,
			TerminationReason: TerminationCompleted,
		}, nil
	}
	return true, Result{Generated: s.generated, TerminationReason: TerminationRepetitive}, ErrRepetitiveOutput
}

// llmErrorAction describes what the caller should do after an LLM error.
type llmErrorAction int

const (
	llmErrorRetry         llmErrorAction = iota // simple retry (sleep if needed)
	llmErrorOverflowRetry                       // retry with aggressive compaction
	llmErrorFatal                               // non-recoverable; return to caller
)

// handleLLMError inspects err and updates loopState. It returns (action, true)
// when the loop should continue with the given action, or (_, false) when the
// error is fatal and should be returned to the caller.
func (r Runner) handleLLMError(ctx context.Context, err error, maxOverloadRetries int, s *loopState, req Request) (llmErrorAction, bool) {
	if llm.IsEmptyResponse(err) && s.retryOnce("empty_response") {
		s.messages = append(s.messages, llm.Message{Role: "system", Content: emptyResponseRetryPrompt()})
		r.updateStatus(req.Locale, RetryStatus)
		return llmErrorRetry, true
	}
	if llm.IsTemporaryOverload(err) && s.overloadRetries < maxOverloadRetries && !s.streamedText {
		s.overloadRetries++
		var pe llm.ProviderError
		var delay time.Duration
		if llm.IsRateLimited(err) && errors.As(err, &pe) && pe.RetryAfter > 0 {
			delay = pe.RetryAfter
		} else {
			delay = llm.RetryDelay(s.overloadRetries)
		}
		if sleepErr := sleepCtx(ctx, delay); sleepErr != nil {
			return llmErrorFatal, false
		}
		r.updateStatus(req.Locale, RetryStatus)
		return llmErrorRetry, true
	}
	if llm.IsPromptTooLong(err) && r.Compactor != nil && s.retryOnce("prompt_too_long") {
		r.updateStatus(req.Locale, RetryStatus)
		return llmErrorOverflowRetry, true
	}
	return llmErrorFatal, false
}

// handleFinalResponse processes a no-tool-call response from the LLM.
//
// Return values:
//   - emitted=true, continueLoop=false, err=nil → answer is ready, return to caller
//   - emitted=false, continueLoop=true,  err=nil → recovery injected, continue step loop
//   - emitted=false, continueLoop=false, err!=nil → non-recoverable, return error to caller
func (r Runner) handleFinalResponse(ctx context.Context, resp llm.Response, assistantMsg *llm.Message, useStream bool, s *loopState, req Request) (emitted bool, continueLoop bool, err error) {
	// max_output_tokens: partial content was streamed; ask the model to resume.
	if isMaxOutputTokensResponse(resp) && s.maxOutputTokensRecoveryCount < maxOutputTokensRecoveryLimit {
		s.maxOutputTokensRecoveryCount++
		partial := strings.TrimSpace(r.sanitize(assistantMsg.Content))
		if partial != "" {
			assistantMsg.Content = partial
			s.messages = append(s.messages, *assistantMsg)
			s.generated = append(s.generated, *assistantMsg)
		}
		s.messages = append(s.messages, llm.Message{Role: "user", Content: maxOutputTokensRecoveryPrompt()})
		r.observeEvent("max_output_tokens_recovery", map[string]any{"attempt": s.maxOutputTokensRecoveryCount})
		r.updateStatus(req.Locale, RetryStatus)
		return false, true, nil
	}

	if !useStream && r.StatusUpdate != nil {
		r.StatusUpdate(GeneratingStatus(req.Locale))
	}

	final := strings.TrimSpace(r.sanitize(assistantMsg.Content))

	if final == "" {
		if s.retryOnce("empty_response") {

			s.messages = append(s.messages, llm.Message{Role: "system", Content: emptyResponseRetryPrompt()})
			r.updateStatus(req.Locale, RetryStatus)
			return false, true, nil
		}
		return false, false, ErrEmptyFinal
	}

	if llm.LooksLikeTextualToolCall(final) {
		if s.retryOnce("textual_tool_call") {

			s.messages = append(s.messages, llm.Message{Role: "system", Content: textualToolCallRetryPrompt()})
			r.updateStatus(req.Locale, RetryStatus)
			return false, true, nil
		}
		final = llm.StripTextualToolCallMarkup(final)
		if final == "" {
			return false, false, ErrTextualToolCall
		}
	}

	if hasRawEvidenceDump(final) {
		if s.retryOnce("raw_evidence") {

			s.messages = append(s.messages, llm.Message{Role: "system", Content: rawEvidenceRetryPrompt()})
			r.updateStatus(req.Locale, RetryStatus)
			return false, true, nil
		}
		final = stripRawEvidenceDump(final)
		if final == "" {
			return false, false, ErrTextualToolCall
		}
	}

	if looksRepetitive(final) {
		if s.retryOnce("repetitive_final") {

			s.messages = append(s.messages, llm.Message{Role: "system", Content: repetitiveRetryPrompt()})
			r.updateStatus(req.Locale, RetryStatus)
			return false, true, nil
		}
		return false, false, ErrRepetitiveOutput
	}

	// Code evidence checks apply in both streaming and non-streaming modes.
	if !s.codeToolCalledThisRun && hasUnverifiedCodeClaim(final) && s.retryOnce("code_claim") {
		s.messages = append(s.messages, llm.Message{Role: "system", Content: codeClaimRetryPrompt()})
		r.updateStatus(req.Locale, RetryStatus)
		return false, true, nil
	}
	if s.codeToolCalledThisRun && hasUnverifiedCodeClaim(final) && hasUnevidencedFileReference(final, s.generated) && s.retryOnce("unevidenced_file") {
		s.messages = append(s.messages, llm.Message{Role: "system", Content: unevidencedFileRetryPrompt()})
		r.updateStatus(req.Locale, RetryStatus)
		return false, true, nil
	}

	assistantMsg.Content = final
	s.messages = append(s.messages, *assistantMsg)
	s.generated = append(s.generated, *assistantMsg)
	return true, false, nil
}

// emitStepStatus updates the status indicator at the start of each step.
func (r Runner) emitStepStatus(locale string, step int, s *loopState) {
	if r.StatusUpdate == nil {
		return
	}
	if r.StatusSummarizer != nil {
		// Always refresh on every step so the native Slack status keeps showing
		// the last known loading message (or the default "thinking" text) while
		// the LLM generates. The summarizer overwrites this once it resolves.
		r.StatusUpdate(ThinkingStatus(locale))
	} else {
		r.StatusUpdate(StepStatus(locale, step))
	}
}

// callLLM invokes the LLM (streaming or non-streaming) and returns the
// response plus a bool indicating whether streaming was actually used.
// Side-effects: updates s.streamedText and s.answerFlushed via callbacks.
func (r Runner) callLLM(ctx context.Context, llmReq llm.Request, s *loopState, req Request) (llm.Response, bool, error) {
	wantStream := r.OnStream != nil
	llmStart := time.Now()

	// Suppress StreamAnswer on retry steps that already flushed the answer.
	stepOnStream := r.OnStream
	if s.answerFlushed && r.OnStream != nil {
		stepOnStream = func(ev StreamEvent) {
			if ev.Kind != StreamAnswer {
				r.OnStream(ev)
			}
		}
	}

	s.streamedText = false
	s.streamRouter = nil
	s.toolStatusStarted = false
	if wantStream {
		// Always route text through streamRouter so text is never emitted
		// directly — the router buffers everything and defers the answer until
		// handleFinalResponse has validated it, eliminating the need for a
		// separate streamGuard.
		s.streamRouter = &streamRouter{emit: stepOnStream, afterTools: s.afterTools}
	}

	streamText := func(delta string) {
		if delta != "" {
			s.streamedText = true
		}
		if s.streamRouter != nil {
			s.streamRouter.text(delta)
		} else if stepOnStream != nil {
			stepOnStream(StreamEvent{Kind: StreamAnswer, Delta: delta})
			s.answerFlushed = true
		}
	}

	// Streaming tool executor: starts executing tools as they arrive.
	var streamExec *streamingToolExecutor
	if wantStream && r.Tools != nil && len(llmReq.Tools) > 0 {
		streamExec = newStreamingToolExecutor(ctx, r, req)
		s.streamExec = streamExec
	}

	streamHandler := llm.StreamHandler{
		OnText: streamText,
		OnToolCallsStarted: func() {
			if s.streamRouter != nil {
				s.streamRouter.toolCallsStarted()
			}
		},
		OnToolCallComplete: func(call llm.ToolCall) {
			r.startToolStatus(ctx, []llm.ToolCall{call}, s, req)
			if streamExec != nil {
				streamExec.Submit(call)
			}
		},
		OnUsage: func(usage llm.Usage) {
			if r.OnUsage != nil {
				r.OnUsage(usage)
			}
		},
	}

	var resp llm.Response
	var err error
	didStream := false
	if wantStream {
		if sc, ok := r.LLM.(llm.StreamClient); ok {
			resp, err = sc.ChatStream(ctx, llmReq, streamHandler)
			didStream = resp.Streamed
		} else {
			resp, err = r.LLM.Chat(ctx, llmReq)
		}
	} else {
		resp, err = r.LLM.Chat(ctx, llmReq)
	}

	if r.Observer != nil {
		llmDuration := time.Since(llmStart)
		if observer, ok := r.Observer.(LLMResponseObserver); ok {
			observer.LLMResponse(resp, llmDuration, err)
		} else {
			r.Observer.LLMCall(resp.Usage, llmDuration, err)
		}
	}
	if err == nil && r.OnUsage != nil && !isZeroUsage(resp.Usage) {
		r.OnUsage(resp.Usage)
	}

	return resp, didStream, err
}

func (r Runner) startToolStatus(ctx context.Context, calls []llm.ToolCall, s *loopState, req Request) {
	if r.StatusSummarizer == nil || s == nil || s.toolStatusStarted || len(calls) == 0 {
		return
	}
	if time.Since(s.lastStatusSummaryAt) < 3*time.Second {
		return
	}
	names := make([]string, 0, len(calls))
	sampleArgs := ""
	for _, call := range calls {
		if call.Function.Name == "" {
			continue
		}
		names = append(names, call.Function.Name)
		if sampleArgs == "" {
			sampleArgs = call.Function.Arguments
		}
	}
	if len(names) == 0 {
		return
	}
	s.toolStatusStarted = true
	s.lastStatusSummaryAt = time.Now()
	update := r.StatusUpdate
	if r.LoadingMessageUpdate != nil {
		update = r.LoadingMessageUpdate
	}
	r.StatusSummarizer.Summarize(ctx, strings.Join(names, ", "), sampleArgs, req.Locale, update)
}

func isZeroUsage(usage llm.Usage) bool {
	return usage.PromptTokens == 0 &&
		usage.CompletionTokens == 0 &&
		usage.TotalTokens == 0 &&
		usage.CacheCreationInputTokens == 0 &&
		usage.CacheReadInputTokens == 0
}

// emitNarration emits non-streamed tool-call narration so the UI shows it.
func (r Runner) emitNarration(assistantMsg llm.Message, s *loopState) {
	narration := strings.TrimSpace(assistantMsg.Content)
	if narration == "" || r.OnStream == nil || s.streamedText {
		return
	}
	if llm.LooksLikeTextualToolCall(narration) {
		return
	}
	if s.afterTools {
		narration = "\n\n" + narration
	}
	r.OnStream(StreamEvent{Kind: StreamNarration, Delta: narration})
}

// runToolCalls executes the tool calls from an assistant message, preferring
// the streaming executor when it has already started executing them.
func (r Runner) runToolCalls(ctx context.Context, calls []llm.ToolCall, s *loopState, req Request) []toolResult {
	if s.streamExec != nil && s.streamExec.HasSubmitted() {
		results := s.streamExec.Drain(calls)
		s.streamExec = nil
		return results
	}
	s.streamExec = nil

	// Circuit breaker: short-circuit calls that have repeatedly failed with
	// the same arguments, or that have succeeded too many times without any
	// observable change (e.g. navigating to the same URL in a loop).
	if s.identicalCallFailures == nil {
		s.identicalCallFailures = make(map[string]int)
	}
	if s.identicalCallSuccesses == nil {
		s.identicalCallSuccesses = make(map[string]int)
	}
	toExecute := make([]llm.ToolCall, 0, len(calls))
	toExecuteIdx := make([]int, 0, len(calls))
	results := make([]toolResult, len(calls))

	for i, call := range calls {
		key := identicalCallKey(call)
		if s.identicalCallFailures[key] >= maxIdenticalFailedCallAttempts {
			blocker := fmt.Errorf("identical failed call blocked: this exact %s call has failed %d consecutive times already",
				call.Function.Name, maxIdenticalFailedCallAttempts)
			results[i] = toolResult{
				message: llm.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Name:       call.Function.Name,
					Content: fmt.Sprintf("[tool error] This exact call already failed %d consecutive times with identical arguments. "+
						"You MUST change your approach: use different parameters, try a different tool, or answer with the information already gathered.",
						maxIdenticalFailedCallAttempts),
				},
				name: call.Function.Name,
				args: json.RawMessage(call.Function.Arguments),
				err:  blocker,
			}
		} else if s.identicalCallSuccesses[key] > maxIdenticalSuccessCallAttempts {
			blocker := fmt.Errorf("identical successful call blocked: this exact %s call has already succeeded %d consecutive times",
				call.Function.Name, s.identicalCallSuccesses[key])
			results[i] = toolResult{
				message: llm.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Name:       call.Function.Name,
					Content: fmt.Sprintf("[tool error] This exact call already succeeded %d consecutive times with identical arguments. "+
						"Do not run it again. You MUST answer now from the information already gathered, or choose a materially different tool/arguments if more evidence is truly necessary.",
						s.identicalCallSuccesses[key]),
				},
				name: call.Function.Name,
				args: json.RawMessage(call.Function.Arguments),
				err:  blocker,
			}
		} else {
			toExecute = append(toExecute, call)
			toExecuteIdx = append(toExecuteIdx, i)
		}
	}

	if len(toExecute) > 0 {
		executed := r.executeToolCalls(ctx, toExecute, req)
		for j, res := range executed {
			idx := toExecuteIdx[j]
			key := identicalCallKey(toExecute[j])
			if res.err != nil {
				s.identicalCallFailures[key]++
				delete(s.identicalCallSuccesses, key)
			} else {
				delete(s.identicalCallFailures, key)
				s.identicalCallSuccesses[key]++
				// Inject one warning into the result when the same successful
				// call has been repeated too many times. A subsequent identical
				// call is blocked before execution above.
				if n := s.identicalCallSuccesses[key]; n == maxIdenticalSuccessCallAttempts+1 {
					warning := fmt.Sprintf("\n\n[agent warning] This exact %s call has now succeeded %d times with identical arguments. "+
						"You appear to be stuck in a loop. You MUST change your approach: try different parameters, use a different tool, "+
						"or synthesize an answer from what you have already gathered.",
						toExecute[j].Function.Name, n)
					res.message.Content += warning
				}
			}
			results[idx] = res
		}
	}
	return results
}

// identicalCallKey returns a stable key for a tool call based on its name and
// a short hash of its arguments. Used by the identical-call circuit breaker.
func identicalCallKey(call llm.ToolCall) string {
	h := sha256.Sum256([]byte(call.Function.Arguments))
	return call.Function.Name + "::" + fmt.Sprintf("%x", h[:4])
}

// handleMaxSteps is called when the agent exhausts its step budget. It makes
// one final tool-free LLM call to force a summary answer.
func (r Runner) handleMaxSteps(ctx context.Context, s *loopState, req Request) (Result, error) {
	if r.StatusUpdate != nil {
		r.StatusUpdate(GeneratingStatus(req.Locale))
	}
	s.messages = r.prepareMessagesForQuery(ctx, s.messages, req, nil)
	s.messages = append(s.messages, llm.Message{
		Role:    "system",
		Content: "You have reached the investigation step limit. Summarize your findings now based on evidence gathered so far. If you are uncertain, state what is known and what remains unverified.",
	})
	resp, err := r.LLM.Chat(ctx, llm.Request{
		Model:       r.Model,
		Messages:    s.messages,
		Tools:       nil,
		MaxTokens:   r.MaxTokens,
		Temperature: r.Temp,
		Thinking:    r.Thinking,
	})
	if err == nil && strings.TrimSpace(resp.Message.Content) != "" {
		final := strings.TrimSpace(r.sanitize(resp.Message.Content))
		msg := resp.Message
		msg.Content = final
		s.generated = append(s.generated, msg)
		return Result{Generated: s.generated, Final: final, TerminationReason: TerminationMaxSteps}, nil
	}
	return Result{Generated: s.generated, TerminationReason: TerminationMaxSteps}, ErrMaxToolSteps
}

// updateStatus is a convenience helper that calls r.StatusUpdate(fn(locale))
// when StatusUpdate is set.
func (r Runner) updateStatus(locale string, fn func(string) string) {
	if r.StatusUpdate != nil {
		r.StatusUpdate(fn(locale))
	}
}

func hasRawEvidenceDump(text string) bool {
	return strings.Contains(text, "<evidence source=") ||
		strings.Contains(text, "</evidence>") ||
		strings.Contains(text, "<tool_call>") ||
		strings.Contains(text, "</tool_call>")
}

func stripRawEvidenceDump(text string) string {
	lines := strings.Split(text, "\n")
	cleaned := make([]string, 0, len(lines))
	skippingBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.Contains(trimmed, "<evidence source="), strings.Contains(trimmed, "<tool_call>"):
			skippingBlock = true
			continue
		case strings.Contains(trimmed, "</evidence>"), strings.Contains(trimmed, "</tool_call>"):
			skippingBlock = false
			continue
		case skippingBlock:
			continue
		case strings.HasPrefix(trimmed, "- [code-"), strings.HasPrefix(trimmed, "[code-"):
			continue
		default:
			cleaned = append(cleaned, line)
		}
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func (r Runner) compactMessages(ctx context.Context, messages []llm.Message, reserveTokens int) []llm.Message {
	if r.Compactor == nil {
		return messages
	}
	// Always apply micro-compact first (zero cost, clears old tool results).
	messages = r.Compactor.ApplyMicroCompact(messages)
	// Only run expensive compaction when near the threshold.
	compacted, result, err := r.Compactor.CompactIfNeededWithReserve(ctx, messages, reserveTokens)
	if err != nil {
		r.observeEvent("compact_error", map[string]any{"error": err.Error(), "mode": "auto"})
		return messages
	}
	r.observeCompactResult(result)
	return compacted
}

func (r Runner) compactMessagesAggressive(ctx context.Context, messages []llm.Message) []llm.Message {
	if r.Compactor == nil {
		return messages
	}
	compacted, result, err := r.Compactor.CompactForce(ctx, messages)
	if err != nil {
		r.observeEvent("compact_error", map[string]any{"error": err.Error(), "mode": "force"})
		return messages
	}
	r.observeCompactResult(result)
	return compacted
}

func (r Runner) observeCompactResult(result *memory.CompactResult) {
	if result == nil || result.Layer == "" {
		return
	}
	meta := map[string]any{
		"layer":       result.Layer,
		"pre_tokens":  result.PreTokens,
		"post_tokens": result.PostTokens,
	}
	if result.CircuitBreakerHit {
		meta["circuit_breaker_hit"] = true
	}
	r.observeEvent("context_compact", meta)
}

func (r Runner) observeEvent(name string, metadata map[string]any) {
	if r.Observer == nil || name == "" {
		return
	}
	if observer, ok := r.Observer.(EventObserver); ok {
		observer.Event(name, metadata)
	}
}

func isMaxOutputTokensResponse(resp llm.Response) bool {
	reason := strings.ToLower(strings.TrimSpace(resp.FinishReason))
	switch reason {
	case "max_tokens", "length", "max_output_tokens":
		return true
	default:
		return strings.Contains(reason, "max") && strings.Contains(reason, "token")
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func requestLocale(locale string) string {
	return strings.TrimSpace(locale)
}

func localeHint(locale string) string {
	switch strings.TrimSpace(locale) {
	case LocaleZH:
		return "用户语言：中文。所有回复必须使用中文，代码、日志原文、文件路径除外。"
	default:
		return ""
	}
}

type toolResult struct {
	message     llm.Message
	waitForUser bool
	name        string
	args        json.RawMessage
	duration    time.Duration
	err         error
}

func (r Runner) executeToolCalls(ctx context.Context, calls []llm.ToolCall, req Request) []toolResult {
	type indexedCall struct {
		index int
		call  llm.ToolCall
	}

	results := make([]toolResult, len(calls))
	var parallelBatch []indexedCall

	flushParallel := func() {
		if len(parallelBatch) == 0 {
			return
		}
		if r.StatusUpdate != nil && r.StatusSummarizer == nil {
			names := make([]string, 0, len(parallelBatch))
			for _, ic := range parallelBatch {
				names = append(names, ic.call.Function.Name)
			}
			r.StatusUpdate(ToolHint(strings.Join(names, ", "), req.Locale))
		}

		sem := make(chan struct{}, maxStreamingConcurrency)
		var wg sync.WaitGroup
		wg.Add(len(parallelBatch))
		for _, ic := range parallelBatch {
			go func(ic indexedCall) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				results[ic.index] = r.executeSingleTool(ctx, ic.call, req, false)
			}(ic)
		}
		wg.Wait()
		parallelBatch = nil
	}

	for i, call := range calls {
		name := call.Function.Name
		ic := indexedCall{index: i, call: call}
		if r.Tools.CanRunInParallel(name) {
			parallelBatch = append(parallelBatch, ic)
		} else {
			flushParallel()
			results[ic.index] = r.executeSingleTool(ctx, ic.call, req, true)
		}
	}
	flushParallel()

	r.observeToolResults(results)
	return results
}

func (r Runner) executeSingleTool(ctx context.Context, call llm.ToolCall, req Request, emitStatus bool) toolResult {
	name := call.Function.Name
	if emitStatus && r.StatusUpdate != nil && r.StatusSummarizer == nil {
		r.StatusUpdate(ToolHint(name, req.Locale))
	}
	args := json.RawMessage(call.Function.Arguments)
	start := time.Now()
	result, err := r.Tools.Execute(ctx, name, args, req.Runtime)
	duration := time.Since(start)
	content := ""
	needsUserInput := result.NeedsUserInput || result.WaitForUser
	if err != nil {
		content = "[tool error] " + err.Error()
	} else if needsUserInput {
		content = r.sanitize(result.Content)
	} else {
		content = r.format(name, maybeSpillResult(spillRunID(req.RunID), name, call.ID, r.sanitize(result.Content)))
	}
	if err != nil || needsUserInput {
		content = maybeSpillResult(spillRunID(req.RunID), name, call.ID, content)
	}
	return toolResult{
		message:     llm.Message{Role: "tool", ToolCallID: call.ID, Name: name, Content: content},
		waitForUser: err == nil && needsUserInput,
		name:        name,
		args:        args,
		duration:    duration,
		err:         err,
	}
}

func (r Runner) observeToolResults(results []toolResult) {
	if r.Observer == nil {
		return
	}
	for _, result := range results {
		if result.name == "" {
			continue
		}
		if observer, ok := r.Observer.(MetadataObserver); ok {
			observer.ToolCallWithMetadata(result.name, result.args, result.duration, result.err)
		} else {
			r.Observer.ToolCall(result.name, result.duration, result.err)
		}
	}
}

func looksRepetitive(text string) bool {
	normalized := strings.TrimSpace(text)
	if len([]rune(normalized)) < 160 {
		return false
	}
	units := repeatedUnits(normalized)
	if len(units) < 8 {
		return false
	}
	counts := map[string]int{}
	total := 0
	for _, unit := range units {
		runeLen := len([]rune(unit))
		if runeLen < 8 {
			continue
		}
		counts[unit]++
		total++
		if counts[unit] >= 6 && counts[unit]*runeLen >= len([]rune(normalized))/3 {
			return true
		}
	}
	if total == 0 {
		return false
	}
	for _, count := range counts {
		if count >= 8 && count*2 >= total {
			return true
		}
	}
	return false
}

func repeatedUnits(text string) []string {
	splitter := regexp.MustCompile(`[。！？!?;\n]+`)
	raw := splitter.Split(text, -1)
	units := make([]string, 0, len(raw))
	for _, unit := range raw {
		unit = strings.Join(strings.Fields(strings.TrimSpace(unit)), " ")
		if unit != "" {
			units = append(units, unit)
		}
	}
	return units
}

func (r Runner) format(toolName, output string) string {
	if r.Format == nil {
		return output
	}
	return r.Format.ToolObservation(toolName, output)
}

func (r Runner) sanitize(text string) string {
	if r.Sanitize == nil {
		return text
	}
	return r.Sanitize.Sanitize(text)
}

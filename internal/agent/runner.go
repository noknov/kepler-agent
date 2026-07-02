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
	"unicode/utf8"

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
	pivotTierGentle = 4  // gentle nudge: "most questions resolve in 1-3 calls"
	pivotTierFirm   = 8  // firm pivot: "answer now or justify continuing"
	pivotTierUrgent = 12 // urgent: "you MUST answer with what you have"
	pivotTierForce  = 16 // force: strip tools, require text answer

	maxOutputTokensRecoveryLimit = 3

	// maxIdenticalCallAttempts is the number of times the same tool can be
	// called with identical arguments before the circuit breaker short-circuits
	// subsequent identical calls with an error. Prevents infinite loops where
	// the model ignores error feedback and keeps repeating the same failing call.
	maxIdenticalCallAttempts = 3
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
	LLM               llm.Client
	Model             string
	Thinking          string
	MaxTokens         int
	Temp              float64
	Tools             *registry.Registry
	Capabilities      llm.Capabilities
	Format            ObservationFormatter
	Sanitize          Sanitizer
	Observer          Observer
	MaxSteps          int
	Compactor         *memory.Compactor
	StatusUpdate      StatusUpdater
	StatusSummarizer  *StatusSummarizer
	OnStream          func(StreamEvent)
	OnUsage           func(llm.Usage)
	OnLLMStepComplete func()
}

type Request struct {
	Messages     []llm.Message
	UserQuestion string
	Runtime      registry.Runtime
	Locale       string
	RunID        string
	Steering     SteeringProvider
}

// TerminationReason describes why the agent loop exited. It is analogous to
// claude-code's Terminal.reason enum and is used for observability and testing.
type TerminationReason string

const (
	TerminationCompleted    TerminationReason = "completed"     // normal text answer
	TerminationPending      TerminationReason = "pending_user"  // waiting for user input
	TerminationMaxSteps     TerminationReason = "max_steps"     // hit step budget
	TerminationCanceled     TerminationReason = "canceled"      // context canceled
	TerminationModelError   TerminationReason = "model_error"   // unrecoverable LLM error
	TerminationEmptyFinal   TerminationReason = "empty_final"   // persistent empty response
	TerminationRepetitive   TerminationReason = "repetitive"    // model looping
	TerminationTextualTool  TerminationReason = "textual_tool"  // model hallucinating tool calls
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

	// retry guards — each is a one-shot flag; once set, the corresponding
	// recovery is exhausted and the next occurrence becomes a hard failure.
	retriedRepetitiveFinal bool
	retriedTextualToolCall bool
	retriedEmptyResponse   bool
	retriedPromptTooLong   bool
	retriedCodeClaim       bool
	retriedUnevidencedFile bool
	retriedRawEvidence     bool

	// overload / max-output-tokens recovery counters
	overloadRetries              int
	maxOutputTokensRecoveryCount int

	// tracking for code-evidence validation
	codeToolCalledThisRun bool

	// afterTools is true once at least one tool result has been received;
	// it governs streaming router behaviour and pivot escalation.
	afterTools bool

	// answerFlushed is true once any StreamAnswer event has been emitted.
	// Subsequent retry steps must not write more text to the answer stream.
	answerFlushed bool

	// streamedText tracks whether any text was emitted in the current step.
	streamedText bool

	// pivotTier tracks progressive escalation pressure (0=none … 4=force).
	pivotTier int

	// clarificationErrors tracks persistent tool failures that require user input.
	clarificationErrors *clarificationErrorTracker

	// identicalCallCounts is the circuit breaker for identical repeated tool
	// calls. Keyed by "toolName::sha256(args)" → attempt count. When a key
	// exceeds maxIdenticalCallAttempts the call is short-circuited with an
	// error instead of being executed, preventing infinite retry loops.
	identicalCallCounts map[string]int

	// per-step streaming components; reset each iteration by callLLM.
	streamRouter *streamRouter
	streamExec   *streamingToolExecutor
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
	return prompts.RunnerPrompt("max_output_tokens_recovery", "Output token limit hit. Resume directly - no apology, no recap of what you were doing. Pick up mid-thought if that is where the cut happened. Break remaining work into smaller pieces.")
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
		maxSteps = 80 // hard ceiling; investigations rarely need more than 30–40 steps
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
	r.applyPivotEscalation(step, s, req)

	if req.Steering != nil {
		if steering := req.Steering(); len(steering) > 0 {
			s.messages = append(s.messages, steering...)
		}
	}

	var toolSpecs []llm.ToolSpec
	if r.Tools != nil && s.pivotTier < 4 {
		toolSpecs = r.Tools.Specs()
	}
	s.messages = r.prepareMessagesForQuery(ctx, s.messages, req, toolSpecs)

	resp, useStream, llmErr := r.callLLM(ctx, llm.Request{
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

	// Force pivot: tools were stripped from the request but some models
	// (e.g. mimo-v2.5) still generate tool_call blocks regardless. Discard
	// them so the response is treated as a text-only final answer.
	if s.pivotTier >= 4 && len(assistantMsg.ToolCalls) > 0 {
		assistantMsg.ToolCalls = nil
	}

	if s.streamRouter != nil {
		s.streamRouter.finish(len(assistantMsg.ToolCalls) > 0)
		// Note: do NOT propagate answerFlushed here. The streamRouter stores
		// the answer as pendingAnswer; s.answerFlushed is set only after
		// commitAnswer() emits it once handleFinalResponse validation passes.
	}
	if useStream && !s.streamedText && strings.TrimSpace(assistantMsg.Content) != "" {
		useStream = false
	}

	if len(assistantMsg.ToolCalls) == 0 {
		return r.settleFinalResponse(ctx, resp, assistantMsg, useStream, s, req)
	}

	// Tool-call turn: emit narration, execute tools, collect results.
	r.emitNarration(assistantMsg, s)
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
	if question := s.clarificationErrors.Question(toolResults, requestLocale(req.Locale)); question != "" {
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
			s.streamRouter.pendingAnswer = ""
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
		if s.streamRouter != nil {
			s.streamRouter.commitAnswer()
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
	llmErrorRetry        llmErrorAction = iota // simple retry (sleep if needed)
	llmErrorOverflowRetry                      // retry with aggressive compaction
	llmErrorFatal                              // non-recoverable; return to caller
)

// handleLLMError inspects err and updates loopState. It returns (action, true)
// when the loop should continue with the given action, or (_, false) when the
// error is fatal and should be returned to the caller.
func (r Runner) handleLLMError(ctx context.Context, err error, maxOverloadRetries int, s *loopState, req Request) (llmErrorAction, bool) {
	if llm.IsEmptyResponse(err) && !s.retriedEmptyResponse {
		s.retriedEmptyResponse = true
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
	if llm.IsPromptTooLong(err) && r.Compactor != nil && !s.retriedPromptTooLong {
		s.retriedPromptTooLong = true
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
		if !s.retriedEmptyResponse {
			s.retriedEmptyResponse = true
			s.messages = append(s.messages, llm.Message{Role: "system", Content: emptyResponseRetryPrompt()})
			r.updateStatus(req.Locale, RetryStatus)
			return false, true, nil
		}
		return false, false, ErrEmptyFinal
	}

	if !useStream && llm.LooksLikeTextualToolCall(final) {
		if !s.retriedTextualToolCall {
			s.retriedTextualToolCall = true
			s.messages = append(s.messages, llm.Message{Role: "system", Content: textualToolCallRetryPrompt()})
			r.updateStatus(req.Locale, RetryStatus)
			return false, true, nil
		}
		final = llm.StripTextualToolCallMarkup(final)
		if final == "" {
			return false, false, ErrTextualToolCall
		}
	}

	if !useStream && hasRawEvidenceDump(final) {
		if !s.retriedRawEvidence {
			s.retriedRawEvidence = true
			s.messages = append(s.messages, llm.Message{Role: "system", Content: rawEvidenceRetryPrompt()})
			r.updateStatus(req.Locale, RetryStatus)
			return false, true, nil
		}
		final = stripRawEvidenceDump(final)
		if final == "" {
			return false, false, ErrTextualToolCall
		}
	}

	if !useStream && looksRepetitive(final) {
		if !s.retriedRepetitiveFinal {
			s.retriedRepetitiveFinal = true
			s.messages = append(s.messages, llm.Message{Role: "system", Content: repetitiveRetryPrompt()})
			r.updateStatus(req.Locale, RetryStatus)
			return false, true, nil
		}
		return false, false, ErrRepetitiveOutput
	}

	// Code evidence checks apply in both streaming and non-streaming modes.
	if !s.retriedCodeClaim && !s.codeToolCalledThisRun && hasUnverifiedCodeClaim(final) {
		s.retriedCodeClaim = true
		s.messages = append(s.messages, llm.Message{Role: "system", Content: codeClaimRetryPrompt()})
		r.updateStatus(req.Locale, RetryStatus)
		return false, true, nil
	}
	if !s.retriedUnevidencedFile && s.codeToolCalledThisRun && hasUnverifiedCodeClaim(final) && hasUnevidencedFileReference(final, s.generated) {
		s.retriedUnevidencedFile = true
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
		if step == 0 {
			r.StatusUpdate(ThinkingStatus(locale))
		}
		// Subsequent steps: the summarizer overwrites the status asynchronously.
	} else {
		r.StatusUpdate(StepStatus(locale, step))
	}
}

// applyPivotEscalation injects progressive-pressure system messages to steer
// the model toward an answer when it has used too many steps.
func (r Runner) applyPivotEscalation(step int, s *loopState, req Request) {
	if !s.afterTools {
		return
	}
	switch {
	case step >= pivotTierForce && s.pivotTier < 4:
		s.pivotTier = 4
		s.messages = append(s.messages, llm.Message{Role: "system", Content: pivotForceMessage()})
		r.observeEvent("pivot_force", map[string]any{"step": step})
	case step >= pivotTierUrgent && s.pivotTier < 3:
		s.pivotTier = 3
		s.messages = append(s.messages, llm.Message{Role: "system", Content: pivotUrgentMessage()})
		r.observeEvent("pivot_urgent", map[string]any{"step": step})
	case step >= pivotTierFirm && s.pivotTier < 2:
		s.pivotTier = 2
		s.messages = append(s.messages, llm.Message{Role: "system", Content: stepBudgetPivotMessage()})
		r.observeEvent("pivot_firm", map[string]any{"step": step})
	case step >= pivotTierGentle && s.pivotTier < 1:
		s.pivotTier = 1
		s.messages = append(s.messages, llm.Message{Role: "system", Content: pivotGentleMessage()})
		r.observeEvent("pivot_gentle", map[string]any{"step": step})
	}
}

// callLLM invokes the LLM (streaming or non-streaming) and returns the
// response plus a bool indicating whether streaming was actually used.
// Side-effects: updates s.streamedText and s.answerFlushed via callbacks.
func (r Runner) callLLM(ctx context.Context, llmReq llm.Request, s *loopState, req Request) (llm.Response, bool, error) {
	useStream := r.OnStream != nil
	llmStart := time.Now()

	// Wrap OnStream to suppress StreamAnswer events after the answer has already
	// been flushed (prevents retry steps from polluting the answer stream).
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
	if useStream && len(llmReq.Tools) > 0 {
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

	// Streaming tool executor: starts executing tools as they arrive, rather
	// than waiting for the full LLM response to complete.
	var streamExec *streamingToolExecutor
	if useStream && r.Tools != nil && len(llmReq.Tools) > 0 {
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
	if useStream {
		if sc, ok := r.LLM.(llm.StreamClient); ok {
			if r.useStreamGuard() {
				threshold := streamGuardThreshold
				if r.Capabilities.NativeToolCalls {
					threshold = streamGuardThresholdNative
				}
				guard := &streamGuard{downstream: streamText, threshold: threshold}
				resp, err = sc.ChatStream(ctx, llmReq, llm.StreamHandler{
					OnText:             guard.Write,
					OnToolCallsStarted: streamHandler.OnToolCallsStarted,
					OnToolCallComplete: streamHandler.OnToolCallComplete,
					OnUsage:            streamHandler.OnUsage,
				})
				guard.Flush()
				if guard.suppressed || !guard.emitted || !resp.Streamed {
					useStream = false
				}
			} else {
				resp, err = sc.ChatStream(ctx, llmReq, streamHandler)
				if !resp.Streamed {
					useStream = false
				}
			}
		} else {
			resp, err = r.LLM.Chat(ctx, llmReq)
			useStream = false
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

	return resp, useStream, err
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

	// Circuit breaker: short-circuit calls that have been attempted with the
	// same arguments more than maxIdenticalCallAttempts times. The model is
	// stuck in a retry loop and executing again will only produce the same
	// error — we inject a synthetic error result instead.
	if s.identicalCallCounts == nil {
		s.identicalCallCounts = make(map[string]int)
	}
	toExecute := make([]llm.ToolCall, 0, len(calls))
	toExecuteIdx := make([]int, 0, len(calls))
	results := make([]toolResult, len(calls))

	for i, call := range calls {
		key := identicalCallKey(call)
		s.identicalCallCounts[key]++
		if s.identicalCallCounts[key] > maxIdenticalCallAttempts {
			blocker := fmt.Errorf("identical call blocked: this exact %s call has been attempted %d times already",
				call.Function.Name, maxIdenticalCallAttempts)
			results[i] = toolResult{
				message: llm.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Name:       call.Function.Name,
					Content: fmt.Sprintf("[tool error] This exact call was already attempted %d times with identical arguments and kept failing. "+
						"You MUST change your approach: use different parameters, try a different tool, or answer with the information already gathered.",
						maxIdenticalCallAttempts),
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
			results[toExecuteIdx[j]] = res
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
	// Fire one dynamic status summary per agent turn (covers the full tool batch).
	// The main loop already shows ThinkingStatus while the LLM generates; the
	// summary overwrites that without an extra intermediate update here.
	if len(calls) > 0 && r.StatusSummarizer != nil {
		names := make([]string, len(calls))
		for i, c := range calls {
			names[i] = c.Function.Name
		}
		r.StatusSummarizer.Summarize(ctx, strings.Join(names, ", "), calls[0].Function.Arguments, req.Locale, r.StatusUpdate)
	}

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
		content = r.format(name, r.sanitize(result.Content))
	}
	content = maybeSpillResult(spillRunID(req.RunID), name, call.ID, content)
	return toolResult{
		message:     llm.Message{Role: "tool", ToolCallID: call.ID, Name: name, Content: content},
		waitForUser: err == nil && needsUserInput,
		name:        name,
		args:        args,
		duration:    duration,
		err:         err,
	}
}

type clarificationErrorTracker struct {
	counts   map[string]int
	failures map[string][]clarificationFailure
}

type clarificationFailure struct {
	Tool  string
	Args  map[string]any
	Error string
}

func newClarificationErrorTracker() *clarificationErrorTracker {
	return &clarificationErrorTracker{
		counts:   map[string]int{},
		failures: map[string][]clarificationFailure{},
	}
}

func (t *clarificationErrorTracker) Question(results []toolResult, locale string) string {
	if t == nil {
		return ""
	}
	t.resetWithSuccessfulEvidence(results)
	for _, result := range results {
		if result.err == nil {
			continue
		}
		key := clarificationErrorKey(result)
		if key == "" {
			continue
		}
		t.counts[key]++
		t.failures[key] = append(t.failures[key], clarificationFailure{
			Tool:  result.name,
			Args:  decodeToolArgs(result.args),
			Error: conciseError(result.err.Error()),
		})
		if t.counts[key] >= clarificationErrorThreshold {
			return clarificationQuestion(locale, key, t.failures[key])
		}
	}
	return ""
}

func (t *clarificationErrorTracker) resetWithSuccessfulEvidence(results []toolResult) {
	for _, result := range results {
		if result.err == nil {
			// Any successful tool call resets all counters — the model is
			// making progress and earlier failures were likely self-corrected.
			t.counts = map[string]int{}
			t.failures = map[string][]clarificationFailure{}
			return
		}
	}
}

func (t *clarificationErrorTracker) resetKeys(keys ...string) {
	for _, key := range keys {
		delete(t.counts, key)
		delete(t.failures, key)
	}
}

func clarificationErrorKey(result toolResult) string {
	errText := strings.ToLower(result.err.Error())
	switch {
	// These are errors the model CANNOT self-correct — they require user input
	// or external action. Everything else (wrong path, wrong branch, wrong repo,
	// invalid params, timeouts) the model should retry with different parameters.
	case strings.Contains(errText, "unauthorized") ||
		strings.Contains(errText, "forbidden") ||
		strings.Contains(errText, "permission denied") ||
		strings.Contains(errText, "access denied") ||
		strings.Contains(errText, "authentication required"):
		return "auth_access"
	case strings.Contains(errText, "missing config") ||
		strings.Contains(errText, "not configured"):
		return "missing_config"
	default:
		return ""
	}
}

func decodeToolArgs(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil
	}
	return args
}

func conciseError(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 180 {
		return string(runes[:180]) + "..."
	}
	return text
}

func clarificationQuestion(locale, key string, failures []clarificationFailure) string {
	zh := strings.HasPrefix(strings.ToLower(locale), "zh")
	// Only two categories reach here — both are genuinely blocked and need user input.
	switch key {
	case "auth_access":
		if zh {
			return "我没有权限访问这个资源，需要额外授权才能继续。"
		}
		return "I don't have permission to access this resource. It needs additional authorization to proceed."
	case "missing_config":
		if zh {
			return "缺少必要的配置，暂时无法执行这个操作。"
		}
		return "A required configuration is missing — I can't perform this operation right now."
	default:
		if zh {
			return "遇到了无法自动解决的问题，需要你的帮助。"
		}
		return "I've hit an issue I can't resolve automatically and need your help."
	}
}

func clarificationDetail(zh bool, key string, failures []clarificationFailure) string {
	return ""
}

func failureNames(failures []clarificationFailure) []string {
	seen := map[string]bool{}
	var names []string
	for _, failure := range failures {
		for _, text := range namedArgValues(failure.Args, []string{"repo", "repository", "path", "branch", "ref", "query", "target", "url", "file", "channel"}) {
			if !seen[text] {
				seen[text] = true
				names = append(names, text)
			}
		}
	}
	if len(names) > 4 {
		return names[:4]
	}
	return names
}

func namedArgValues(args map[string]any, keys []string) []string {
	var values []string
	for _, key := range keys {
		value, ok := args[key]
		if !ok {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text == "" || text == "<nil>" {
			continue
		}
		values = append(values, text)
	}
	return values
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

func pivotGentleMessage() string {
	return prompts.RunnerPrompt("pivot_gentle", "You have used several tool calls. Most questions resolve in 1-3 calls. If you already have enough evidence, answer now. If not, make your next call count — use the single most decisive search or read.")
}

func stepBudgetPivotMessage() string {
	return prompts.RunnerPrompt("pivot_firm", "You have used many investigation steps without answering. STOP broadening your search. Summarize your findings now: what is known, what is uncertain, and the single next check. If you cannot find what you are looking for, say so — do not keep trying variations of the same search.")
}

func pivotUrgentMessage() string {
	return prompts.RunnerPrompt("pivot_urgent", "URGENT: You have spent too many steps on this investigation. You MUST provide an answer NOW with whatever evidence you have gathered. Further searching is very unlikely to help and is degrading the user experience. Summarize findings, state uncertainties, and stop.")
}

func pivotForceMessage() string {
	return prompts.RunnerPrompt("pivot_force", "FINAL: Tools are no longer available. Give your answer now using only the evidence already gathered. Be direct and concise.")
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

// streamRouter buffers ambiguous post-tool text until tool calls start or the
// turn ends, then emits typed StreamEvents. Only used when afterTools is true
// (i.e. the second+ LLM round with tools available) where we cannot know
// upfront whether text is narration or a final answer.
//
// Deferred answer flushing: when the model produces text without tool calls
// (potential final answer), the text is stored in pendingAnswer rather than
// immediately emitted. Only commitAnswer() actually writes it to the output
// stream. This prevents incomplete or invalid intermediate answers from
// appearing in the Slack thread before handleFinalResponse validation passes.
type streamRouter struct {
	emit          func(StreamEvent)
	afterTools    bool
	buf           strings.Builder
	toolTurn      bool
	answerFlushed bool
	pendingAnswer string // validated-and-ready text; set by finish(), emitted by commitAnswer()
}

func (sr *streamRouter) text(delta string) {
	if delta == "" {
		return
	}
	// Always buffer — never emit text deltas directly during streaming.
	// The buffer is flushed as a whole in finish/toolCallsStarted, where
	// we can detect and strip textual tool-call markup.
	sr.buf.WriteString(delta)
}

func (sr *streamRouter) toolCallsStarted() {
	if sr.toolTurn {
		return
	}
	sr.toolTurn = true
	sr.flushAs(StreamNarration)
}

func (sr *streamRouter) finish(hasToolCalls bool) {
	if sr.toolTurn || hasToolCalls {
		if !sr.toolTurn {
			sr.flushAs(StreamNarration)
		}
		return
	}
	sr.flushAs(StreamAnswer)
}

// commitAnswer emits the deferred pending answer to the output stream.
// Must be called only after handleFinalResponse has validated the answer.
func (sr *streamRouter) commitAnswer() {
	if sr.pendingAnswer == "" {
		return
	}
	text := sr.pendingAnswer
	sr.pendingAnswer = ""
	sr.answerFlushed = true
	sr.emit(StreamEvent{Kind: StreamAnswer, Delta: text})
}

func (sr *streamRouter) flushAs(kind StreamKind) {
	if sr.buf.Len() == 0 {
		return
	}
	text := strings.TrimSpace(sr.buf.String())
	sr.buf.Reset()
	if text == "" {
		return
	}
	if llm.LooksLikeTextualToolCall(text) {
		text = strings.TrimSpace(llm.StripTextualToolCallMarkup(text))
		if text == "" {
			return
		}
	}
	if kind == StreamNarration && sr.afterTools {
		text = "\n\n" + text
	}
	if kind == StreamAnswer {
		// Defer: store for commitAnswer() rather than emitting immediately.
		// handleFinalResponse will call commitAnswer() once the response passes
		// all validation checks (code evidence, repetition, textual tool calls, etc.).
		sr.pendingAnswer = text
		return
	}
	sr.emit(StreamEvent{Kind: kind, Delta: text})
}

func (r Runner) useStreamGuard() bool {
	// Native tool call providers (e.g. MiMo, OpenAI) use structured tool_calls
	// fields and do not emit textual markup — skip the guard entirely unless
	// RepairTextualToolCalls is also set, in which case we still guard but
	// use smaller thresholds (see streamGuardThresholdNative) to avoid
	// delaying short Chinese answers.
	if r.Capabilities.NativeToolCalls {
		return r.Capabilities.RepairTextualToolCalls
	}
	if r.Capabilities.RepairTextualToolCalls {
		return true
	}
	return r.Capabilities.Provider == "" && r.Capabilities.Protocol == ""
}

// streamGuard buffers initial tokens and suppresses downstream delivery if
// the content looks like a textual tool call (e.g. <tool_invocation ...>).
// threshold controls how many bytes must accumulate before flushing begins;
// a smaller value means shorter answers stream sooner.
type streamGuard struct {
	downstream llm.StreamCallback
	threshold  int
	buf        strings.Builder
	emitted    bool
	suppressed bool
}

const (
	// streamGuardThreshold is the byte-count of buffered text before the guard
	// begins forwarding to downstream. Large enough to detect markup prefixes
	// like "<tool_invocation" (16 chars) but small enough that short Chinese
	// answers (~10 chars = 30 bytes) still stream promptly.
	streamGuardThreshold = 32

	// streamGuardThresholdNative is used when NativeToolCalls=true. Since
	// native providers use structured tool_calls fields, textual markup is rare
	// and the guard only needs a tiny buffer to catch it at the very start.
	streamGuardThresholdNative = 2

	// streamGuardTail is the byte-count kept buffered after each flush to
	// ensure MayBecomeTextualToolCall can detect a partial markup prefix at
	// the end of the emitted text.
	streamGuardTail = 16
)

func (g *streamGuard) Write(delta string) {
	if g.suppressed || delta == "" {
		return
	}
	g.buf.WriteString(delta)
	g.flushSafePrefix(false)
}

func (g *streamGuard) flushSafePrefix(force bool) {
	if g.buf.Len() == 0 || g.suppressed {
		return
	}
	text := g.buf.String()
	if llm.LooksLikeTextualToolCall(text) {
		g.suppressed = true
		g.buf.Reset()
		return
	}
	threshold := g.threshold
	if threshold <= 0 {
		threshold = streamGuardThreshold
	}
	if !force && (g.buf.Len() < threshold || llm.MayBecomeTextualToolCall(text)) {
		return
	}

	flushLen := len(text)
	if !force && flushLen > streamGuardTail {
		flushLen -= streamGuardTail
	}
	flushLen = utf8SafeCut(text, flushLen)
	if flushLen <= 0 {
		return
	}
	chunk := text[:flushLen]
	rest := text[flushLen:]
	g.downstream(chunk)
	g.emitted = true
	g.buf.Reset()
	if rest != "" {
		g.buf.WriteString(rest)
	}
}

// Flush delivers any buffered content that hasn't been flushed yet.
// Must be called after the stream ends to handle short responses.
func (g *streamGuard) Flush() {
	if g.suppressed {
		return
	}
	g.flushSafePrefix(true)
}

// utf8SafeCut returns the largest prefix length <= maxBytes that does not split
// a UTF-8 code point. Prevents replacement-character corruption when the
// stream guard flushes buffered CJK text at byte boundaries.
func utf8SafeCut(s string, maxBytes int) int {
	if maxBytes <= 0 {
		return 0
	}
	if maxBytes >= len(s) {
		return len(s)
	}
	for maxBytes > 0 && !utf8.ValidString(s[:maxBytes]) {
		maxBytes--
	}
	return maxBytes
}

package agent

import (
	"context"
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
	ErrRepeatedToolCall = errors.New("model repeated the same tool call")
	ErrTextualToolCall  = errors.New("model returned textual tool invocation instead of structured tool calls")
	ErrMaxToolSteps     = errors.New("agent exceeded max tool steps")
	ErrEmptyFinal       = errors.New("model returned empty final response")
)

const (
	// Only trigger clarification after many repeated failures — give the model
	// ample opportunity to self-correct first. Most tool errors (wrong path,
	// wrong branch, wrong repo) are technical mistakes the model can fix itself.
	clarificationErrorThreshold = 4
	exploreFailureLimit         = 3

	// Progressive escalation thresholds. Pressure increases at each tier
	// to prevent aimless searching while still allowing genuine investigations.
	pivotTierGentle = 4  // gentle nudge: "most questions resolve in 1-3 calls"
	pivotTierFirm   = 8  // firm pivot: "answer now or justify continuing"
	pivotTierUrgent = 12 // urgent: "you MUST answer with what you have"
	pivotTierForce  = 16 // force: strip tools, require text answer

	maxOutputTokensRecoveryLimit = 3
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

type Result struct {
	Generated       []llm.Message
	Final           string
	Pending         bool
	PendingQuestion string
	Streamed        bool
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

func planRequiredRetryPrompt() string {
	return prompts.RunnerPrompt("plan_required_retry", "This is a complex multi-step task. Before continuing, call plan-update with concrete investigation steps and exactly one in_progress item. Do not answer directly until the plan exists and you have executed the next evidence-gathering step.")
}

func evidenceRequiredRetryPrompt() string {
	return prompts.RunnerPrompt("evidence_required_retry", "You are about to answer an investigation or debugging question without tool evidence from this run. First gather evidence with targeted read/search/log/runbook/code-intelligence tools. Do not give root causes, architecture judgments, or code behavior claims from memory alone.")
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

var evidenceTools = map[string]bool{
	"code-search":                true,
	"code-read_file":             true,
	"code-symbols":               true,
	"code-definition":            true,
	"code-references":            true,
	"code-diagnostics":           true,
	"git-status":                 true,
	"git-log":                    true,
	"git-show":                   true,
	"git-fetch_ref":              true,
	"git-search_ref":             true,
	"git-read_file_ref":          true,
	"repo-search":                true,
	"repo-read_file":             true,
	"rag-search":                 true,
	"knowledge-runbook_search":   true,
	"web-search":                 true,
	"web-read_page":              true,
	"gcp-logs":                   true,
	"github-workflow_runs":       true,
	"github-pr_diff":             true,
	"slack-file_search":          true,
	"slack-json_analyze":         true,
	"diagnostics-incident_brief": true,
	"diagnostics-timeline":       true,
	"diagnostics-evidence_board": true,
	"explore-code":               true,
	"delegate-run":               true,
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
		maxSteps = 200 // effectively unlimited; context window is the real constraint
	}
	if req.Runtime.Cache == nil {
		req.Runtime.Cache = registry.NewRuntimeCache()
	}
	if strings.TrimSpace(req.RunID) == "" {
		req.RunID = runs.NewID()
	}
	messages := append([]llm.Message(nil), req.Messages...)
	var generated []llm.Message
	seenToolCalls := map[string]int{}
	seenSearchTerms := map[string]int{}
	retriedRepetitiveFinal := false
	retriedTextualToolCall := false
	retriedEmptyResponse := false
	overloadRetries := 0
	const maxOverloadRetries = 3
	retriedPromptTooLong := false
	retriedCodeClaim := false
	retriedUnevidencedFile := false
	retriedPlanRequired := false
	retriedEvidenceRequired := false
	retriedRawEvidence := false
	maxOutputTokensRecoveryCount := 0
	codeToolCalledThisRun := false
	evidenceToolCalledThisRun := false
	planCalledThisRun := false
	planRequired := requiresPlan(req)
	evidenceRequired := requiresEvidence(req)
	afterTools := false
	pivotTier := 0 // 0=none, 1=gentle, 2=firm, 3=urgent, 4=force
	searchMissPivotInjected := false
	consecutiveNoMatchSearchRounds := 0
	toolsWithoutProgress := 0
	clarificationErrors := newClarificationErrorTracker()
	control := newRunnerControl()

	for step := 0; step < maxSteps; step++ {
		if r.StatusUpdate != nil {
			r.StatusUpdate(StepStatus(req.Locale, step))
		}
		// Progressive escalation: increasingly strong pressure to stop.
		if afterTools {
			if step >= pivotTierForce && pivotTier < 4 {
				// Strip tools entirely — force text answer on next LLM call.
				pivotTier = 4
				messages = append(messages, llm.Message{Role: "system", Content: pivotForceMessage()})
				r.observeEvent("pivot_force", map[string]any{"step": step})
			} else if step >= pivotTierUrgent && pivotTier < 3 {
				pivotTier = 3
				messages = append(messages, llm.Message{Role: "system", Content: pivotUrgentMessage()})
				r.observeEvent("pivot_urgent", map[string]any{"step": step})
			} else if step >= pivotTierFirm && pivotTier < 2 {
				pivotTier = 2
				messages = append(messages, llm.Message{Role: "system", Content: stepBudgetPivotMessage()})
				r.observeEvent("pivot_firm", map[string]any{"step": step})
			} else if step >= pivotTierGentle && pivotTier < 1 {
				pivotTier = 1
				messages = append(messages, llm.Message{Role: "system", Content: pivotGentleMessage()})
				r.observeEvent("pivot_gentle", map[string]any{"step": step})
			}
		}
		if req.Steering != nil {
			if steering := req.Steering(); len(steering) > 0 {
				messages = append(messages, steering...)
			}
		}

		messages = r.prepareMessagesForQuery(ctx, messages, req)

		var toolSpecs []llm.ToolSpec
		if r.Tools != nil && pivotTier < 4 {
			toolSpecs = r.selectToolSpecs(control.filterToolSpecs(r.Tools.Specs()), req, messages)
		}

		llmReq := llm.Request{
			Model:       r.Model,
			Messages:    messages,
			Tools:       toolSpecs,
			MaxTokens:   r.MaxTokens,
			Temperature: r.Temp,
			Thinking:    r.Thinking,
		}

		useStream := r.OnStream != nil
		llmStart := time.Now()
		var resp llm.Response
		var err error
		streamedText := false
		var router *streamRouter
		if useStream && len(toolSpecs) > 0 {
			router = &streamRouter{emit: r.OnStream, afterTools: afterTools}
		}
		streamText := func(delta string) {
			if delta != "" {
				streamedText = true
			}
			if router != nil {
				router.text(delta)
			} else {
				r.OnStream(StreamEvent{Kind: StreamAnswer, Delta: delta})
			}
		}

		// Streaming tool executor: start executing tools as they complete
		// during streaming, rather than waiting for the full response.
		var streamExec *streamingToolExecutor
		if useStream && r.Tools != nil && len(toolSpecs) > 0 {
			streamExec = newStreamingToolExecutor(ctx, r, req, seenToolCalls, seenSearchTerms)
		}

		streamHandler := llm.StreamHandler{
			OnText: streamText,
			OnToolCallsStarted: func() {
				if router != nil {
					router.toolCallsStarted()
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
		if useStream {
			if sc, ok := r.LLM.(llm.StreamClient); ok {
				if r.useStreamGuard() {
					guard := &streamGuard{downstream: streamText}
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
		if r.OnLLMStepComplete != nil {
			r.OnLLMStepComplete()
		}
		if err != nil {
			if llm.IsEmptyResponse(err) && !retriedEmptyResponse {
				retriedEmptyResponse = true
				messages = append(messages, llm.Message{Role: "system", Content: emptyResponseRetryPrompt()})
				if r.StatusUpdate != nil {
					r.StatusUpdate(RetryStatus(req.Locale))
				}
				continue
			}
			if llm.IsTemporaryOverload(err) && overloadRetries < maxOverloadRetries && !streamedText {
				overloadRetries++
				if llm.IsRateLimited(err) {
					var pe llm.ProviderError
					if errors.As(err, &pe) && pe.RetryAfter > 0 {
						if sleepErr := sleepCtx(ctx, pe.RetryAfter); sleepErr != nil {
							return Result{Generated: generated}, sleepErr
						}
					} else if sleepErr := sleepCtx(ctx, llm.RetryDelay(overloadRetries)); sleepErr != nil {
						return Result{Generated: generated}, sleepErr
					}
				} else if sleepErr := sleepCtx(ctx, llm.RetryDelay(overloadRetries)); sleepErr != nil {
					return Result{Generated: generated}, sleepErr
				}
				if r.StatusUpdate != nil {
					r.StatusUpdate(RetryStatus(req.Locale))
				}
				continue
			}
			if llm.IsPromptTooLong(err) && r.Compactor != nil && !retriedPromptTooLong {
				retriedPromptTooLong = true
				messages = r.prepareMessagesForOverflowRetry(ctx, messages, req)
				if r.StatusUpdate != nil {
					r.StatusUpdate(RetryStatus(req.Locale))
				}
				continue
			}
			return Result{Generated: generated}, err
		}

		assistantMsg := resp.Message
		assistantMsg.Usage = &resp.Usage // attach API-reported token usage for calibration
		assistantMsg.ToolCalls = memory.NormalizeToolCalls(assistantMsg.ToolCalls)
		if router != nil {
			router.finish(len(assistantMsg.ToolCalls) > 0)
		}
		if useStream && !streamedText && strings.TrimSpace(assistantMsg.Content) != "" {
			useStream = false
		}
		if len(assistantMsg.ToolCalls) == 0 {
			if isMaxOutputTokensResponse(resp) && maxOutputTokensRecoveryCount < maxOutputTokensRecoveryLimit {
				maxOutputTokensRecoveryCount++
				partial := strings.TrimSpace(r.sanitize(assistantMsg.Content))
				if partial != "" {
					assistantMsg.Content = partial
					messages = append(messages, assistantMsg)
					generated = append(generated, assistantMsg)
				}
				messages = append(messages, llm.Message{
					Role:    "user",
					Content: maxOutputTokensRecoveryPrompt(),
				})
				r.observeEvent("max_output_tokens_recovery", map[string]any{"attempt": maxOutputTokensRecoveryCount})
				if r.StatusUpdate != nil {
					r.StatusUpdate(RetryStatus(req.Locale))
				}
				continue
			}
			if !useStream && r.StatusUpdate != nil {
				r.StatusUpdate(GeneratingStatus(req.Locale))
			}
			final := strings.TrimSpace(r.sanitize(assistantMsg.Content))
			if final == "" {
				if !retriedEmptyResponse {
					retriedEmptyResponse = true
					messages = append(messages, llm.Message{Role: "system", Content: emptyResponseRetryPrompt()})
					if r.StatusUpdate != nil {
						r.StatusUpdate(RetryStatus(req.Locale))
					}
					continue
				}
				return Result{Generated: generated}, ErrEmptyFinal
			}
			if !useStream && llm.LooksLikeTextualToolCall(final) {
				if !retriedTextualToolCall {
					retriedTextualToolCall = true
					messages = append(messages, llm.Message{Role: "system", Content: textualToolCallRetryPrompt()})
					if r.StatusUpdate != nil {
						r.StatusUpdate(RetryStatus(req.Locale))
					}
					continue
				}
				final = llm.StripTextualToolCallMarkup(final)
				if final == "" {
					return Result{Generated: generated}, ErrTextualToolCall
				}
			}
			if !useStream && hasRawEvidenceDump(final) {
				if !retriedRawEvidence {
					retriedRawEvidence = true
					messages = append(messages, llm.Message{Role: "system", Content: rawEvidenceRetryPrompt()})
					if r.StatusUpdate != nil {
						r.StatusUpdate(RetryStatus(req.Locale))
					}
					continue
				}
				final = stripRawEvidenceDump(final)
				if final == "" {
					return Result{Generated: generated}, ErrTextualToolCall
				}
			}
			if !useStream && looksRepetitive(final) {
				if !retriedRepetitiveFinal {
					retriedRepetitiveFinal = true
					messages = append(messages, llm.Message{Role: "system", Content: repetitiveRetryPrompt()})
					if r.StatusUpdate != nil {
						r.StatusUpdate(RetryStatus(req.Locale))
					}
					continue
				}
				return Result{Generated: generated}, ErrRepetitiveOutput
			}
			if planRequired && !planCalledThisRun && !retriedPlanRequired {
				retriedPlanRequired = true
				messages = append(messages, llm.Message{Role: "system", Content: planRequiredRetryPrompt()})
				if r.StatusUpdate != nil {
					r.StatusUpdate(RetryStatus(req.Locale))
				}
				continue
			}
			if evidenceRequired && !evidenceToolCalledThisRun && !retriedEvidenceRequired {
				retriedEvidenceRequired = true
				messages = append(messages, llm.Message{Role: "system", Content: evidenceRequiredRetryPrompt()})
				if r.StatusUpdate != nil {
					r.StatusUpdate(RetryStatus(req.Locale))
				}
				continue
			}
			// Unverified code claim check: applies in both streaming and
			// non-streaming modes. If the model references specific code
			// (functions, guards, conditionals) without having called any
			// code tool, force it to verify with actual tool calls first.
			if !retriedCodeClaim && !codeToolCalledThisRun && hasUnverifiedCodeClaim(final) {
				retriedCodeClaim = true
				messages = append(messages, llm.Message{Role: "system", Content: codeClaimRetryPrompt()})
				if r.StatusUpdate != nil {
					r.StatusUpdate(RetryStatus(req.Locale))
				}
				continue
			}
			// Unevidenced file reference check: the model called code tools
			// but its answer references files that never appeared in tool
			// results. This catches the case where the model read file A
			// but makes conclusions about behavior in file B.
			if !retriedUnevidencedFile && codeToolCalledThisRun && hasUnverifiedCodeClaim(final) && hasUnevidencedFileReference(final, generated) {
				retriedUnevidencedFile = true
				messages = append(messages, llm.Message{Role: "system", Content: unevidencedFileRetryPrompt()})
				if r.StatusUpdate != nil {
					r.StatusUpdate(RetryStatus(req.Locale))
				}
				continue
			}
			assistantMsg.Content = final
			messages = append(messages, assistantMsg)
			generated = append(generated, assistantMsg)
			return Result{Generated: generated, Final: final, Streamed: useStream}, nil
		}

		// Emit non-streamed narration as a batch event so the UI still shows it.
		if narration := strings.TrimSpace(assistantMsg.Content); narration != "" && r.OnStream != nil && !streamedText {
			if !llm.LooksLikeTextualToolCall(narration) {
				if afterTools {
					narration = "\n\n" + narration
				}
				r.OnStream(StreamEvent{Kind: StreamNarration, Delta: narration})
			}
		}
		assistantMsg.Content = ""
		messages = append(messages, assistantMsg)
		generated = append(generated, assistantMsg)

		var toolResults []toolResult
		if streamExec != nil && streamExec.HasSubmitted() {
			toolResults = streamExec.Drain(assistantMsg.ToolCalls)
		} else {
			toolResults = r.executeToolCalls(ctx, assistantMsg.ToolCalls, seenToolCalls, seenSearchTerms, req)
		}
		if len(toolResults) > 0 {
			afterTools = true
		}
		allSearchesMissed := searchRoundAllNoMatch(toolResults)
		searchesHadHits := searchRoundHasHits(toolResults)
		if toolResultsLackProgress(toolResults) {
			toolsWithoutProgress++
			// Accelerate escalation when tools aren't producing useful results.
			if toolsWithoutProgress >= 3 && pivotTier < 2 {
				pivotTier = 2
				messages = append(messages, llm.Message{Role: "system", Content: stepBudgetPivotMessage()})
				r.observeEvent("tool_progress_pivot", map[string]any{"rounds_without_progress": toolsWithoutProgress})
			} else if toolsWithoutProgress >= 5 && pivotTier < 3 {
				pivotTier = 3
				messages = append(messages, llm.Message{Role: "system", Content: pivotUrgentMessage()})
				r.observeEvent("tool_progress_urgent", map[string]any{"rounds_without_progress": toolsWithoutProgress})
			}
		} else if len(toolResults) > 0 {
			toolsWithoutProgress = 0
		}
		for _, tr := range toolResults {
			messages = append(messages, tr.message)
			generated = append(generated, tr.message)
			if codeReadingTools[tr.name] {
				codeToolCalledThisRun = true
			}
			if tr.name == "plan-update" && tr.err == nil {
				planCalledThisRun = true
			}
			if evidenceTools[tr.name] && tr.err == nil {
				evidenceToolCalledThisRun = true
			}
			if tr.waitForUser {
				return Result{
					Generated:       generated,
					Pending:         true,
					PendingQuestion: tr.message.Content,
				}, nil
			}
		}
		if allSearchesMissed {
			consecutiveNoMatchSearchRounds++
			if consecutiveNoMatchSearchRounds >= 2 && !searchMissPivotInjected {
				searchMissPivotInjected = true
				messages = append(messages, llm.Message{Role: "system", Content: searchMissPivotMessage()})
				r.observeEvent("search_miss_pivot", map[string]any{"consecutive_no_match_rounds": consecutiveNoMatchSearchRounds})
				// Accelerate escalation when searches keep missing.
				if pivotTier < 1 {
					pivotTier = 1
				}
			}
			// 3+ consecutive misses: force firm pivot.
			if consecutiveNoMatchSearchRounds >= 3 && pivotTier < 2 {
				pivotTier = 2
				messages = append(messages, llm.Message{Role: "system", Content: stepBudgetPivotMessage()})
				r.observeEvent("search_miss_firm", map[string]any{"consecutive_no_match_rounds": consecutiveNoMatchSearchRounds})
			}
		} else if searchesHadHits {
			consecutiveNoMatchSearchRounds = 0
		}
		control.finishTurn(toolResults)
		locale := requestLocale(req.Locale)
		if question := clarificationErrors.Question(toolResults, locale); question != "" {
			generated = append(generated, llm.Message{
				Role:    "assistant",
				Content: question,
			})
			return Result{
				Generated:       generated,
				Pending:         true,
				PendingQuestion: question,
			}, nil
		}
	}
	// Max steps reached: attempt one final LLM call without tools to force a
	// summary answer rather than returning a hard error to the user.
	if r.StatusUpdate != nil {
		r.StatusUpdate(GeneratingStatus(req.Locale))
	}
	messages = r.prepareMessagesForQuery(ctx, messages, req)
	messages = append(messages, llm.Message{Role: "system", Content: "You have reached the investigation step limit. Summarize your findings now based on evidence gathered so far. If you are uncertain, state what is known and what remains unverified."})
	resp, err := r.LLM.Chat(ctx, llm.Request{
		Model:       r.Model,
		Messages:    messages,
		Tools:       nil, // no tools — force text answer
		MaxTokens:   r.MaxTokens,
		Temperature: r.Temp,
		Thinking:    r.Thinking,
	})
	if err == nil && strings.TrimSpace(resp.Message.Content) != "" {
		final := strings.TrimSpace(r.sanitize(resp.Message.Content))
		msg := resp.Message
		msg.Content = final
		generated = append(generated, msg)
		return Result{Generated: generated, Final: final}, nil
	}
	return Result{Generated: generated}, ErrMaxToolSteps
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

func (r Runner) compactMessages(ctx context.Context, messages []llm.Message) []llm.Message {
	if r.Compactor == nil {
		return messages
	}
	// Always apply micro-compact first (zero cost, clears old tool results).
	messages = r.Compactor.ApplyMicroCompact(messages)
	// Only run expensive compaction when near the threshold.
	compacted, result, err := r.Compactor.CompactIfNeeded(ctx, messages)
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

const defaultToolSpecLimit = 18

var coreToolNames = map[string]bool{
	"tool_search":    true,
	"plan-update":    true,
	"code-search":    true,
	"code-read_file": true,
	"git-status":     true,
	"git-log":        true,
	"git-show":       true,
	"skills-load":    true,
	"slack-ask_user": true,
}

var intentToolHints = []struct {
	terms []string
	tools []string
}{
	{[]string{"github", "workflow", "ci", "action", "pull request", "pr", "deploy", "工作流", "流水线", "构建", "部署", "拉取请求"}, []string{"github-workflow_runs", "github-pr_diff", "github-dispatch_workflow"}},
	{[]string{"branch", "commit", "ref", "revision", "sha", "tag", "remote", "分支", "提交", "版本", "远程"}, []string{"repo-search", "repo-read_file", "git-fetch_ref", "git-search_ref", "git-read_file_ref"}},
	{[]string{"log", "gcp", "cloud logging", "k8s", "gke", "pod", "namespace", "error rate", "日志", "报错", "错误率", "命名空间", "集群"}, []string{"gcp-logs", "diagnostics-incident_brief", "diagnostics-timeline", "diagnostics-evidence_board"}},
	{[]string{"web", "url", "http", "docs", "page", "search internet", "网页", "网站", "文档", "搜索", "互联网"}, []string{"web-search", "web-read_page"}},
	{[]string{"slack", "file", "screenshot", "json", "文件", "截图", "表格", "附件"}, []string{"slack-file_search", "slack-json_analyze", "slack-send_screenshot"}},
	{[]string{"browser", "playwright", "click", "screenshot", "ui", "page", "浏览器", "点击", "页面", "截图", "表单"}, []string{"pw-navigate", "pw-snapshot", "pw-click", "pw-type", "pw-fill_form", "pw-screenshot", "pw-get_all_pages", "pw-switch_page", "pw-evaluate"}},
	{[]string{"notion", "youtrack", "ticket", "issue", "工单", "需求", "缺陷", "问题单"}, []string{"notion-search", "youtrack-search", "youtrack-get_issue"}},
	{[]string{"symbol", "definition", "reference", "diagnostic", "lsp", "符号", "定义", "引用", "诊断"}, []string{"code-symbols", "code-definition", "code-references", "code-diagnostics"}},
	{[]string{"rag", "semantic", "embedding", "runbook", "语义", "向量", "预案", "手册"}, []string{"rag-search", "knowledge-runbook_search"}},
	{[]string{"architecture", "architectural", "refactor", "redesign", "compare", "performance", "accuracy", "agent", "broad", "multi-step", "架构", "重构", "对比", "性能", "效率", "准确", "体验", "效果", "复杂"}, []string{"plan-update", "explore-code", "delegate-run", "rag-search", "code-symbols", "code-definition", "code-references"}},
}

func (r Runner) selectToolSpecs(specs []llm.ToolSpec, req Request, messages []llm.Message) []llm.ToolSpec {
	if len(specs) <= defaultToolSpecLimit {
		return specs
	}
	allowed := map[string]bool{}
	for name := range coreToolNames {
		allowed[name] = true
	}
	intent := strings.ToLower(req.UserQuestion)
	if strings.TrimSpace(intent) == "" {
		intent = strings.ToLower(lastUserContent(messages))
	}
	for _, hint := range intentToolHints {
		if containsAny(intent, hint.terms) {
			for _, name := range hint.tools {
				allowed[name] = true
			}
		}
	}

	out := make([]llm.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		name := spec.Function.Name
		if allowed[name] {
			out = append(out, spec)
		}
	}
	if len(out) == 0 {
		return specs
	}
	if len(out) < len(specs) {
		r.observeEvent("tool_surface_pruned", map[string]any{
			"before": len(specs),
			"after":  len(out),
		})
	}
	return out
}

func lastUserContent(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

func containsAny(text string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func requiresPlan(req Request) bool {
	text := requestIntentText(req)
	if text == "" {
		return false
	}
	return containsAny(text, []string{
		"architecture", "architectural", "refactor", "redesign", "compare", "performance", "accuracy",
		"multi-step", "migration", "全面", "架构", "重构", "对比", "准确", "效率", "性能",
		"复杂", "设计", "迁移",
	})
}

func requiresEvidence(req Request) bool {
	text := requestIntentText(req)
	if text == "" {
		return false
	}
	return containsAny(text, []string{
		"why", "root cause", "bug", "broken", "error", "exception", "incident", "regression", "debug",
		"investigate", "trace", "diagnose", "architecture", "performance", "accuracy", "compare",
		"what happens", "will it", "does it", "is it", "when does", "how does",
		"为什么", "原因", "根因", "报错", "错误", "异常", "故障", "事故", "回归", "排查",
		"查 bug", "查问题", "诊断", "架构", "性能", "准确", "对比",
		"会不会", "是否", "如果", "什么时候", "怎么", "会自动",
	})
}

func requestIntentText(req Request) string {
	text := strings.ToLower(strings.TrimSpace(req.UserQuestion))
	if text != "" {
		return text
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return strings.ToLower(req.Messages[i].Content)
		}
	}
	return ""
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

type runnerControl struct {
	toolFailures  map[string]int
	disabledTools map[string]bool
}

func newRunnerControl() *runnerControl {
	return &runnerControl{
		toolFailures:  map[string]int{},
		disabledTools: map[string]bool{},
	}
}

func (c *runnerControl) filterToolSpecs(specs []llm.ToolSpec) []llm.ToolSpec {
	if c == nil || len(c.disabledTools) == 0 {
		return specs
	}
	out := make([]llm.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		if c.disabledTools[spec.Function.Name] {
			continue
		}
		out = append(out, spec)
	}
	return out
}

func (c *runnerControl) finishTurn(results []toolResult) {
	if c == nil || len(results) == 0 {
		return
	}
	for _, result := range results {
		if result.err != nil {
			c.toolFailures[result.name]++
			if result.name == "explore-code" && c.toolFailures[result.name] >= exploreFailureLimit {
				c.disabledTools[result.name] = true
			}
			continue
		}
		if result.name != "" {
			c.toolFailures[result.name] = 0
		}
	}
}

func requestLocale(locale string) string {
	return strings.TrimSpace(locale)
}

type toolResult struct {
	message     llm.Message
	waitForUser bool
	name        string
	args        json.RawMessage
	duration    time.Duration
	err         error
}

func (r Runner) executeToolCalls(ctx context.Context, calls []llm.ToolCall, seenToolCalls, seenSearchTerms map[string]int, req Request) []toolResult {
	// Fire one dynamic status summary per agent turn (covers the full tool batch).
	// The static ToolHint shown later may be overwritten when the LLM responds.
	if len(calls) > 0 {
		names := make([]string, len(calls))
		for i, c := range calls {
			names[i] = c.Function.Name
		}
		sample := calls[0].Function.Arguments
		r.StatusSummarizer.Summarize(ctx, strings.Join(names, ", "), sample, req.Locale, r.StatusUpdate)
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
		if r.StatusUpdate != nil {
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
		if dup, content := r.duplicateToolCall(call, seenToolCalls, seenSearchTerms); dup {
			err := fmt.Errorf("%w: %s", ErrRepeatedToolCall, name)
			results[i] = toolResult{
				message: llm.Message{Role: "tool", ToolCallID: call.ID, Name: name, Content: content},
				name:    name,
				err:     err,
			}
			continue
		}
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
	if emitStatus && r.StatusUpdate != nil {
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

func toolCallSignature(call llm.ToolCall) string {
	args := strings.Join(strings.Fields(call.Function.Arguments), " ")
	return call.Function.Name + "\x00" + args
}

var searchDedupTools = map[string]bool{
	"code-search":              true,
	"repo-search":              true,
	"git-search_ref":           true,
	"rag-search":               true,
	"knowledge-runbook_search": true,
	"notion-search":            true,
	"web-search":               true,
	"slack-file_search":        true,
}

func normalizeSearchTerm(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

func searchTermSignature(toolName string, args json.RawMessage) string {
	if !searchDedupTools[toolName] {
		return ""
	}
	var payload struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &payload); err != nil || strings.TrimSpace(payload.Query) == "" {
		return ""
	}
	return toolName + "\x00" + normalizeSearchTerm(payload.Query)
}

func (r Runner) duplicateToolCall(call llm.ToolCall, seenToolCalls, seenSearchTerms map[string]int) (bool, string) {
	name := call.Function.Name
	repeatable := r.Tools != nil && r.Tools.IsRepeatable(name)
	signature := toolCallSignature(call)
	seenToolCalls[signature]++
	if seenToolCalls[signature] > 2 && !repeatable {
		return true, fmt.Sprintf("[tool error] duplicate %s call skipped. Use the existing tool result already in the conversation, call a different tool with different arguments, or give the final answer now.", name)
	}
	if searchSig := searchTermSignature(name, json.RawMessage(call.Function.Arguments)); searchSig != "" {
		seenSearchTerms[searchSig]++
		if seenSearchTerms[searchSig] > 2 && !repeatable {
			return true, fmt.Sprintf("[tool error] duplicate %s call skipped. Use the existing tool result already in the conversation, call a different tool with different arguments, or give the final answer now.", name)
		}
	}
	return false, ""
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

func searchMissPivotMessage() string {
	return prompts.RunnerPrompt("search_miss_pivot", "Recent searches returned no matches. Before repeating similar search terms, diagnose the failed assumption: wrong repository/root, wrong branch/ref, wrong path, wrong product wording, generated code, missing external tool, or unavailable data source. Try a different evidence source or a meaningfully different naming pattern. If that still misses, answer with what is known and what remains unverified instead of continuing to guess.")
}

func toolResultsLackProgress(results []toolResult) bool {
	if len(results) == 0 {
		return false
	}
	bad := 0
	for _, result := range results {
		if result.err != nil || emptyToolResult(result.message.Content) {
			bad++
		}
	}
	return bad*2 >= len(results)
}

func emptyToolResult(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return true
	}
	if strings.HasPrefix(content, "[tool error]") {
		return true
	}
	switch content {
	case "no matches", "no web results":
		return true
	}
	return strings.HasPrefix(content, "no matching code found")
}

func unwrapEvidenceContent(content string) string {
	content = strings.TrimSpace(content)
	if !strings.Contains(content, "<evidence") {
		return content
	}
	start := strings.Index(content, ">")
	end := strings.LastIndex(content, "</evidence>")
	if start < 0 || end <= start {
		return content
	}
	return strings.TrimSpace(content[start+1 : end])
}

func isNoMatchResult(content string) bool {
	inner := unwrapEvidenceContent(content)
	switch inner {
	case "", "no matches", "no web results":
		return true
	}
	return strings.HasPrefix(inner, "no matching code found")
}

func searchRoundAllNoMatch(results []toolResult) bool {
	if len(results) == 0 {
		return false
	}
	searches := 0
	for _, result := range results {
		if !searchDedupTools[result.name] {
			continue
		}
		searches++
		if !isNoMatchResult(result.message.Content) {
			return false
		}
	}
	return searches > 0
}

func searchRoundHasHits(results []toolResult) bool {
	for _, result := range results {
		if !searchDedupTools[result.name] {
			continue
		}
		if !isNoMatchResult(result.message.Content) {
			return true
		}
	}
	return false
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
type streamRouter struct {
	emit       func(StreamEvent)
	afterTools bool
	buf        strings.Builder
	toolTurn   bool
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
	sr.emit(StreamEvent{Kind: kind, Delta: text})
}

func (r Runner) useStreamGuard() bool {
	// Active when RepairTextualToolCalls is set. Even Anthropic-compatible
	// providers (MiMo) may emit tool-call markup in text blocks, so the
	// guard must be on for all providers to prevent leaking markup to users.
	if r.Capabilities.RepairTextualToolCalls {
		return true
	}
	if r.Capabilities.NativeToolCalls {
		return false
	}
	return r.Capabilities.Provider == "" && r.Capabilities.Protocol == ""
}

// streamGuard buffers initial tokens and suppresses downstream delivery if
// the content looks like a textual tool call (e.g. <tool_invocation ...>).
type streamGuard struct {
	downstream llm.StreamCallback
	buf        strings.Builder
	emitted    bool
	suppressed bool
}

const (
	streamGuardThreshold = 160
	streamGuardTail      = 96
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
	if !force && (g.buf.Len() < streamGuardThreshold || llm.MayBecomeTextualToolCall(text)) {
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

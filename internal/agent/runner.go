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

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/memory"
	"github.com/wati/oncall-agent/internal/prompts"
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
	clarificationErrorThreshold = 2
	convergenceNudgeCodeTurns   = 12
	convergenceNudgeSearchTurns = 8
	convergenceNudgeRAGTurns    = 5
	forceSynthesisCodeTurns     = 18
	forceSynthesisSearchTurns   = 12
	noProgressForceTurns        = 4
	maxSynthesisRetries         = 2
	exploreFailureLimit         = 1
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
	OnStream     func(StreamEvent)
	OnUsage      func(llm.Usage)
}

type Request struct {
	Messages []llm.Message
	Runtime  registry.Runtime
	Locale   string
	Steering SteeringProvider
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

func budgetWarningPrompt(remainingToolSteps int) string {
	tmpl := prompts.RunnerPrompt("budget_warning", "")
	return fmt.Sprintf(tmpl, remainingToolSteps)
}

func codeClaimRetryPrompt() string {
	return prompts.RunnerPrompt("code_claim_retry", "")
}

func convergenceWarningPrompt() string {
	return prompts.RunnerPrompt("convergence_warning", "")
}

func synthesisPrompt() string {
	return prompts.RunnerPrompt("synthesis_now", "")
}

func synthesisRetryPrompt() string {
	return prompts.RunnerPrompt("synthesis_retry", "")
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
	"rag-search":        true,
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

func (r Runner) Run(ctx context.Context, req Request) (Result, error) {
	maxSteps := r.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 12
	}
	if req.Runtime.Cache == nil {
		req.Runtime.Cache = registry.NewRuntimeCache()
	}
	messages := append([]llm.Message(nil), req.Messages...)
	var generated []llm.Message
	seenToolCalls := map[string]int{}
	retriedRepetitiveFinal := false
	retriedTextualToolCall := false
	retriedEmptyResponse := false
	retriedTemporaryOverload := false
	retriedCodeClaim := false
	retriedRawEvidence := false
	codeToolCalledThisRun := false
	budgetWarned := false
	convergenceWarned := false
	synthesisRetries := 0
	afterTools := false
	clarificationErrors := newClarificationErrorTracker()
	progress := convergenceProgress{searchTurns: map[string]int{}}
	control := newRunnerControl()

	for step := 0; step < maxSteps; step++ {
		lastStep := step == maxSteps-1
		if r.StatusUpdate != nil {
			r.StatusUpdate(StepStatus(req.Locale, step))
		}
		if req.Steering != nil {
			if steering := req.Steering(); len(steering) > 0 {
				messages = append(messages, steering...)
			}
		}

		if !budgetWarned && step == maxSteps-10 && !lastStep {
			remaining := (maxSteps - 1) - step
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: budgetWarningPrompt(remaining),
			})
			budgetWarned = true
		}

		forceSynthesis := !lastStep && control.shouldForceSynthesis(progress)
		if forceSynthesis {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: synthesisPrompt(),
			})
		} else if !convergenceWarned && progress.shouldNudge() && !lastStep {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: convergenceWarningPrompt(),
			})
			convergenceWarned = true
		}

		if r.Compactor != nil {
			messages = r.compactMessages(ctx, messages)
		}

		var toolSpecs []llm.ToolSpec
		if !lastStep && !forceSynthesis {
			toolSpecs = control.filterToolSpecs(r.Tools.Specs())
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
		streamHandler := llm.StreamHandler{
			OnText: streamText,
			OnToolCallsStarted: func() {
				if router != nil {
					router.toolCallsStarted()
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
						OnUsage:            streamHandler.OnUsage,
					})
					guard.Flush()
					if guard.suppressed || !resp.Streamed {
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
		if err != nil {
			if llm.IsEmptyResponse(err) && !retriedEmptyResponse {
				retriedEmptyResponse = true
				messages = append(messages, llm.Message{Role: "system", Content: emptyResponseRetryPrompt()})
				if r.StatusUpdate != nil {
					r.StatusUpdate(RetryStatus(req.Locale))
				}
				continue
			}
			if llm.IsTemporaryOverload(err) && !retriedTemporaryOverload && !streamedText {
				retriedTemporaryOverload = true
				if r.StatusUpdate != nil {
					r.StatusUpdate(RetryStatus(req.Locale))
				}
				continue
			}
			return Result{Generated: generated}, err
		}

		assistantMsg := resp.Message
		assistantMsg.Usage = &resp.Usage // attach API-reported token usage for calibration
		if router != nil {
			router.finish(len(assistantMsg.ToolCalls) > 0)
		}
		if useStream && !streamedText && strings.TrimSpace(assistantMsg.Content) != "" {
			useStream = false
		}
		if len(assistantMsg.ToolCalls) == 0 {
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
			if !useStream && !retriedCodeClaim && !codeToolCalledThisRun && hasUnverifiedCodeClaim(final) {
				retriedCodeClaim = true
				messages = append(messages, llm.Message{Role: "system", Content: codeClaimRetryPrompt()})
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

		if len(toolSpecs) == 0 {
			synthesisRetries++
			if synthesisRetries <= maxSynthesisRetries {
				messages = append(messages, llm.Message{Role: "system", Content: synthesisRetryPrompt()})
				if r.StatusUpdate != nil {
					r.StatusUpdate(RetryStatus(req.Locale))
				}
				continue
			}
			return Result{Generated: generated}, ErrMaxToolSteps
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

		toolResults := r.executeToolCalls(ctx, assistantMsg.ToolCalls, seenToolCalls, req)
		if len(toolResults) > 0 {
			afterTools = true
		}
		for _, tr := range toolResults {
			messages = append(messages, tr.message)
			generated = append(generated, tr.message)
			if codeReadingTools[tr.name] {
				codeToolCalledThisRun = true
			}
			if tr.waitForUser {
				return Result{
					Generated:       generated,
					Pending:         true,
					PendingQuestion: tr.message.Content,
				}, nil
			}
		}
		progress.finishTurn(toolResults)
		control.finishTurn(toolResults)
		locale := requestLocale(req.Locale, messages)
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
	compacted, _, err := r.Compactor.CompactIfNeeded(ctx, messages)
	if err != nil {
		return r.Compactor.ApplyMicroCompact(messages)
	}
	return compacted
}

type runnerControl struct {
	seenEvidence    map[string]bool
	noProgressTurns int
	toolFailures    map[string]int
	disabledTools   map[string]bool
}

func newRunnerControl() *runnerControl {
	return &runnerControl{
		seenEvidence:  map[string]bool{},
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
	newEvidence := false
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
		if !codeReadingTools[result.name] {
			continue
		}
		fp := evidenceFingerprint(result)
		if fp == "" || c.seenEvidence[fp] {
			continue
		}
		c.seenEvidence[fp] = true
		newEvidence = true
	}
	if newEvidence {
		c.noProgressTurns = 0
		return
	}
	c.noProgressTurns++
}

func (c *runnerControl) shouldForceSynthesis(progress convergenceProgress) bool {
	if c == nil {
		return false
	}
	if c.noProgressTurns >= noProgressForceTurns {
		return true
	}
	if progress.codeEvidenceTurns >= forceSynthesisCodeTurns {
		return true
	}
	return progress.searchTurns["code-search"] >= forceSynthesisSearchTurns ||
		progress.searchTurns["repo-search"] >= forceSynthesisSearchTurns ||
		progress.searchTurns["rag-search"] >= convergenceNudgeRAGTurns+2
}

func evidenceFingerprint(result toolResult) string {
	if result.name == "" {
		return ""
	}
	content := strings.Join(strings.Fields(result.message.Content), " ")
	if content == "" {
		content = strings.Join(strings.Fields(string(result.args)), " ")
	}
	if content == "" {
		return ""
	}
	if len(content) > 512 {
		content = content[:512]
	}
	return result.name + "\x00" + content
}

type convergenceProgress struct {
	codeEvidenceTurns int
	searchTurns       map[string]int
}

func (p *convergenceProgress) finishTurn(results []toolResult) {
	if p == nil || len(results) == 0 {
		return
	}
	if p.searchTurns == nil {
		p.searchTurns = map[string]int{}
	}
	hadCodeEvidence := false
	searchTypes := map[string]bool{}
	for _, result := range results {
		if result.err != nil {
			continue
		}
		if codeReadingTools[result.name] {
			hadCodeEvidence = true
		}
		switch result.name {
		case "code-search", "repo-search", "rag-search":
			searchTypes[result.name] = true
		}
	}
	if hadCodeEvidence {
		p.codeEvidenceTurns++
	}
	for name := range searchTypes {
		p.searchTurns[name]++
	}
}

func (p convergenceProgress) shouldNudge() bool {
	if p.codeEvidenceTurns >= convergenceNudgeCodeTurns {
		return true
	}
	return p.searchTurns["code-search"] >= convergenceNudgeSearchTurns ||
		p.searchTurns["repo-search"] >= convergenceNudgeSearchTurns ||
		p.searchTurns["rag-search"] >= convergenceNudgeRAGTurns
}

func requestLocale(locale string, messages []llm.Message) string {
	if strings.TrimSpace(locale) != "" {
		return locale
	}
	for _, msg := range messages {
		if msg.Role == "user" && containsCJK(msg.Content) {
			return "zh"
		}
	}
	return locale
}

func containsCJK(text string) bool {
	for _, r := range text {
		if (r >= '\u4e00' && r <= '\u9fff') || (r >= '\u3400' && r <= '\u4dbf') {
			return true
		}
	}
	return false
}

type toolResult struct {
	message     llm.Message
	waitForUser bool
	name        string
	args        json.RawMessage
	duration    time.Duration
	err         error
}

func (r Runner) executeToolCalls(ctx context.Context, calls []llm.ToolCall, seenToolCalls map[string]int, req Request) []toolResult {
	type indexedCall struct {
		index int
		call  llm.ToolCall
	}

	results := make([]toolResult, len(calls))
	var toRun []indexedCall

	for i, call := range calls {
		name := call.Function.Name
		signature := toolCallSignature(call)
		seenToolCalls[signature]++
		if seenToolCalls[signature] > 2 && !r.Tools.IsRepeatable(name) {
			err := fmt.Errorf("%w: %s", ErrRepeatedToolCall, name)
			content := fmt.Sprintf("[tool error] duplicate %s call skipped. Use the existing tool result already in the conversation, call a different tool with different arguments, or give the final answer now.", name)
			results[i] = toolResult{
				message: llm.Message{Role: "tool", ToolCallID: call.ID, Name: name, Content: content},
				name:    name,
				err:     err,
			}
			continue
		}
		toRun = append(toRun, indexedCall{index: i, call: call})
	}

	if len(toRun) == 0 {
		r.observeToolResults(results)
		return results
	}

	allParallel := len(toRun) > 1
	if allParallel {
		for _, ic := range toRun {
			if !r.Tools.CanRunInParallel(ic.call.Function.Name) {
				allParallel = false
				break
			}
		}
	}

	if !allParallel {
		for _, ic := range toRun {
			results[ic.index] = r.executeSingleTool(ctx, ic.call, req, true)
		}
		r.observeToolResults(results)
		return results
	}

	if r.StatusUpdate != nil {
		names := make([]string, 0, len(toRun))
		for _, ic := range toRun {
			names = append(names, ic.call.Function.Name)
		}
		r.StatusUpdate(ToolHint(strings.Join(names, ", "), req.Locale))
	}

	var wg sync.WaitGroup
	wg.Add(len(toRun))
	for _, ic := range toRun {
		go func(ic indexedCall) {
			defer wg.Done()
			results[ic.index] = r.executeSingleTool(ctx, ic.call, req, false)
		}(ic)
	}
	wg.Wait()
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
	if err != nil {
		content = "[tool error] " + err.Error()
	} else if result.WaitForUser {
		content = r.sanitize(result.Content)
	} else {
		content = r.format(name, r.sanitize(result.Content))
	}
	return toolResult{
		message:     llm.Message{Role: "tool", ToolCallID: call.ID, Name: name, Content: content},
		waitForUser: err == nil && result.WaitForUser,
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
	codeEvidence := false
	anySuccess := false
	for _, result := range results {
		if result.err != nil {
			continue
		}
		anySuccess = true
		if codeReadingTools[result.name] {
			codeEvidence = true
		}
	}
	if codeEvidence {
		t.resetKeys("target_lookup", "branch", "git_repository")
	}
	if anySuccess {
		t.resetKeys("invalid_input", "external_unavailable")
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
	case strings.Contains(errText, "file not found in any workspace root") ||
		strings.Contains(errText, "no such file or directory") ||
		strings.Contains(errText, "path ") && strings.Contains(errText, " not in "):
		return "target_lookup"
	case strings.Contains(errText, "branch ") && strings.Contains(errText, "does not exist"):
		return "branch"
	case strings.Contains(errText, "不是 git 仓库") || strings.Contains(errText, "not a git repository"):
		return "git_repository"
	case strings.Contains(errText, "unauthorized") ||
		strings.Contains(errText, "forbidden") ||
		strings.Contains(errText, "permission denied") ||
		strings.Contains(errText, "access denied") ||
		strings.Contains(errText, "authentication required") ||
		strings.Contains(errText, "credentials") ||
		strings.Contains(errText, "token"):
		return "auth_access"
	case strings.Contains(errText, "missing config") ||
		strings.Contains(errText, "not configured") ||
		strings.Contains(errText, "api key") ||
		strings.Contains(errText, "environment variable"):
		return "missing_config"
	case strings.Contains(errText, "invalid ") ||
		strings.Contains(errText, " is required") ||
		strings.Contains(errText, "must be "):
		return "invalid_input"
	case strings.Contains(errText, "timeout") ||
		strings.Contains(errText, "deadline exceeded") ||
		strings.Contains(errText, "connection refused") ||
		strings.Contains(errText, "no such host") ||
		strings.Contains(errText, "temporary failure"):
		return "external_unavailable"
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
	detail := clarificationDetail(zh, key, failures)
	switch key {
	case "target_lookup":
		if zh {
			return detail + "请选一个方向：\n1. 告诉我正确的仓库名、目录名或分支名；\n2. 让我先列出当前可用的仓库和目录再继续；\n3. 先基于已找到的证据给出有限结论。"
		}
		return detail + "Please choose one direction:\n1. Send the correct repository, directory, or branch name;\n2. Have me list the available repositories and directories first;\n3. Have me give a limited conclusion from the evidence already found."
	case "branch":
		if zh {
			return detail + "请选一个方向：\n1. 确认正确分支名；\n2. 让我列出可用远端分支；\n3. 改用默认分支继续。"
		}
		return detail + "Please choose one direction:\n1. Confirm the branch name;\n2. Have me list available remote branches;\n3. Continue with the default branch."
	case "git_repository":
		if zh {
			return detail + "请选一个方向：\n1. 告诉我要分析的仓库位置；\n2. 让我列出当前可用 workspace；\n3. 暂停 git 相关检查，只分析已有上下文。"
		}
		return detail + "Please choose one direction:\n1. Send the repository location;\n2. Have me list the available workspace roots;\n3. Pause git checks and analyze only the existing context."
	case "auth_access":
		if zh {
			return detail + "请选一个方向：\n1. 修复权限后让我重试；\n2. 换一个我有权限访问的地方；\n3. 先基于当前证据给出有限结论。"
		}
		return detail + "Please choose one direction:\n1. Fix access and have me retry;\n2. Switch to a place I can access;\n3. Have me give a limited conclusion from current evidence."
	case "missing_config":
		if zh {
			return detail + "请选一个方向：\n1. 补充缺失配置后让我重试；\n2. 换一个不依赖该配置的检查方式；\n3. 先停止并说明缺了什么。"
		}
		return detail + "Please choose one direction:\n1. Add the missing config and have me retry;\n2. Switch to a check that does not need it;\n3. Stop and summarize what is missing."
	case "invalid_input":
		if zh {
			return detail + "请选一个方向：\n1. 给我准确参数；\n2. 让我先查询可用参数/目标；\n3. 基于当前信息给出有限结论。"
		}
		return detail + "Please choose one direction:\n1. Send the exact input;\n2. Have me discover valid inputs/targets first;\n3. Have me give a limited conclusion from current information."
	case "external_unavailable":
		if zh {
			return detail + "请选一个方向：\n1. 稍后重试；\n2. 换一个数据源/环境；\n3. 先基于已有证据给出有限结论。"
		}
		return detail + "Please choose one direction:\n1. Retry later;\n2. Use a different data source/environment;\n3. Have me give a limited conclusion from the evidence already gathered."
	default:
		if zh {
			return detail + "请选一个方向：\n1. 补充准确名称或条件；\n2. 让我列出可用选项；\n3. 先基于已有证据给出有限结论。"
		}
		return detail + "Please choose one direction:\n1. Provide the exact name or condition;\n2. Have me list available options;\n3. Have me give a limited conclusion from the evidence already gathered."
	}
}

func clarificationDetail(zh bool, key string, failures []clarificationFailure) string {
	names := failureNames(failures)
	if len(names) > 0 {
		joinedZH := strings.Join(names, "、")
		joinedEN := strings.Join(names, ", ")
		switch key {
		case "target_lookup":
			if zh {
				return "我刚才尝试打开或定位 " + joinedZH + "，但这些具体文件/位置没有成功解析。\n"
			}
			return "I tried to open or locate " + joinedEN + ", but those specific files or locations did not resolve.\n"
		case "branch":
			if zh {
				return "我刚才尝试查找 " + joinedZH + " 这些分支，但没有找到匹配。\n"
			}
			return "I tried to find these branches but found no match: " + joinedEN + ".\n"
		case "auth_access":
			if zh {
				return "我刚才尝试访问 " + joinedZH + "，但都遇到权限或认证问题。\n"
			}
			return "I tried to access " + joinedEN + ", but authorization or authentication failed.\n"
		case "invalid_input":
			if zh {
				return "我刚才尝试使用 " + joinedZH + "，但工具连续拒绝这些输入。\n"
			}
			return "I tried " + joinedEN + ", but the tool kept rejecting those inputs.\n"
		}
	}
	if len(failures) == 0 {
		if zh {
			return "我缺少一个具体信息才能继续。\n"
		}
		return "I need one specific detail before continuing.\n"
	}
	last := failures[len(failures)-1]
	if zh {
		return fmt.Sprintf("我连续在 %s 上遇到问题：%s。\n", last.Tool, last.Error)
	}
	return fmt.Sprintf("I keep hitting this problem in %s: %s.\n", last.Tool, last.Error)
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
	if sr.toolTurn {
		sr.emit(StreamEvent{Kind: StreamNarration, Delta: delta})
		return
	}
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
	if sr.buf.Len() == 0 {
		return
	}
	sr.emit(StreamEvent{Kind: StreamAnswer, Delta: sr.buf.String()})
	sr.buf.Reset()
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
	if sr.afterTools {
		text = "\n\n" + text
	}
	sr.emit(StreamEvent{Kind: kind, Delta: text})
}

func (r Runner) useStreamGuard() bool {
	// RepairTextualToolCalls takes priority: even models that support native
	// tool calls can occasionally fall back to textual markup, so the guard
	// must be active to suppress that output before it reaches the user.
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
	flushed    bool
	suppressed bool
}

const streamGuardThreshold = 24

func (g *streamGuard) Write(delta string) {
	if g.suppressed {
		return
	}
	if g.flushed {
		g.downstream(delta)
		return
	}
	g.buf.WriteString(delta)
	if g.buf.Len() >= streamGuardThreshold {
		if llm.LooksLikeTextualToolCall(g.buf.String()) {
			g.suppressed = true
			return
		}
		g.flushed = true
		g.downstream(g.buf.String())
		g.buf.Reset()
	}
}

// Flush delivers any buffered content that hasn't been flushed yet.
// Must be called after the stream ends to handle short responses.
func (g *streamGuard) Flush() {
	if g.suppressed || g.flushed {
		return
	}
	if g.buf.Len() > 0 {
		if llm.LooksLikeTextualToolCall(g.buf.String()) {
			g.suppressed = true
			return
		}
		g.flushed = true
		g.downstream(g.buf.String())
		g.buf.Reset()
	}
}

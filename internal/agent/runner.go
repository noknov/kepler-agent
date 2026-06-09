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
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

var (
	ErrRepetitiveOutput = errors.New("model output repeated itself")
	ErrRepeatedToolCall = errors.New("model repeated the same tool call")
	ErrTextualToolCall  = errors.New("model returned textual tool invocation instead of structured tool calls")
	ErrMaxToolSteps     = errors.New("agent exceeded max tool steps")
)

type Observer interface {
	LLMCall(usage llm.Usage, d time.Duration, err error)
	ToolCall(name string, d time.Duration, err error)
}

type MetadataObserver interface {
	ToolCallWithMetadata(name string, args json.RawMessage, d time.Duration, err error)
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
	LLM             llm.Client
	Model           string
	Thinking        string
	MaxTokens       int
	Temp            float64
	Tools           *registry.Registry
	Format          ObservationFormatter
	Sanitize        Sanitizer
	Observer        Observer
	MaxSteps        int
	MaxContextChars int
	StatusUpdate    StatusUpdater
	OnToken         llm.StreamCallback
	OnNarration     llm.StreamCallback
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

const repetitiveRetryPrompt = "Your previous answer became repetitive. Give one concise final answer only. Do not repeat sentences. Do not narrate further investigation. If evidence is insufficient, say the next check in one short paragraph."

const textualToolCallRetryPrompt = "Your previous reply included textual tool-call markup (for example <tool_call> or <function=...>) instead of using the API's structured tool calling. Do not output tool XML or pseudo tool syntax. Either call tools through the provided tool interface, or give a concise final answer in plain language using evidence already gathered."

func budgetWarningPrompt(remainingToolSteps int) string {
	return fmt.Sprintf(
		"You have %d tool-using turn(s) remaining before you must give your final answer. Stop exploring. Synthesize your findings now using evidence already gathered. Do not start new searches or delegate-run calls unless absolutely critical.",
		remainingToolSteps,
	)
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
	budgetWarned := false

	for step := 0; step < maxSteps; step++ {
		lastStep := step == maxSteps-1 || retriedRepetitiveFinal || retriedTextualToolCall
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

		messages = r.compressContext(messages)

		// On the last step, omit tools so the model is forced to produce a
		// final text response using whatever it has gathered so far.
		var toolSpecs []llm.ToolSpec
		if !lastStep {
			toolSpecs = r.Tools.Specs()
		}

		llmReq := llm.Request{
			Model:       r.Model,
			Messages:    messages,
			Tools:       toolSpecs,
			MaxTokens:   r.MaxTokens,
			Temperature: r.Temp,
			Thinking:    r.Thinking,
		}

		useStream := lastStep && r.OnToken != nil
		llmStart := time.Now()
		var resp llm.Response
		var err error
		if useStream {
			if sc, ok := r.LLM.(llm.StreamClient); ok {
				guard := &streamGuard{downstream: r.OnToken}
				resp, err = sc.ChatStream(ctx, llmReq, guard.Write)
				guard.Flush()
				if guard.suppressed {
					useStream = false
				}
			} else {
				resp, err = r.LLM.Chat(ctx, llmReq)
				useStream = false
			}
		} else {
			resp, err = r.LLM.Chat(ctx, llmReq)
		}
		if r.Observer != nil {
			r.Observer.LLMCall(resp.Usage, time.Since(llmStart), err)
		}
		if err != nil {
			return Result{Generated: generated}, err
		}

		assistantMsg := resp.Message
		if len(assistantMsg.ToolCalls) == 0 {
			if !useStream && r.StatusUpdate != nil {
				r.StatusUpdate(GeneratingStatus(req.Locale))
			}
			final := strings.TrimSpace(r.sanitize(assistantMsg.Content))
			if final == "" {
				final = "I didn't get a valid response. Please try again or provide more context."
			}
			if !useStream && llm.LooksLikeTextualToolCall(final) {
				if !retriedTextualToolCall {
					retriedTextualToolCall = true
					messages = append(messages, llm.Message{Role: "system", Content: textualToolCallRetryPrompt})
					if r.StatusUpdate != nil {
						r.StatusUpdate(RetryStatus(req.Locale))
					}
					continue
				}
				return Result{Generated: generated}, ErrTextualToolCall
			}
			if !useStream && looksRepetitive(final) {
				if !retriedRepetitiveFinal {
					retriedRepetitiveFinal = true
					messages = append(messages, llm.Message{Role: "system", Content: repetitiveRetryPrompt})
					if r.StatusUpdate != nil {
						r.StatusUpdate(RetryStatus(req.Locale))
					}
					continue
				}
				return Result{Generated: generated}, ErrRepetitiveOutput
			}
			assistantMsg.Content = final
			messages = append(messages, assistantMsg)
			generated = append(generated, assistantMsg)
			return Result{Generated: generated, Final: final, Streamed: useStream}, nil
		}

		// Stream any progress text the model emitted before tool calls to the
		// user, then strip it from the conversation history to avoid polluting
		// future turns or amplifying model loops.
		if narration := strings.TrimSpace(assistantMsg.Content); narration != "" && r.OnNarration != nil {
			if !llm.LooksLikeTextualToolCall(narration) {
				r.OnNarration(narration + "\n\n")
			}
		}
		assistantMsg.Content = ""
		messages = append(messages, assistantMsg)
		generated = append(generated, assistantMsg)

		toolResults := r.executeToolCalls(ctx, assistantMsg.ToolCalls, seenToolCalls, req)
		for _, tr := range toolResults {
			messages = append(messages, tr.message)
			generated = append(generated, tr.message)
			if tr.waitForUser {
				return Result{
					Generated:       generated,
					Pending:         true,
					PendingQuestion: tr.message.Content,
				}, nil
			}
		}
	}
	return Result{Generated: generated}, ErrMaxToolSteps
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

	if len(toRun) > 1 {
		for _, ic := range toRun {
			if !r.Tools.CanRunInParallel(ic.call.Function.Name) {
				for _, ic := range toRun {
					results[ic.index] = r.executeSingleTool(ctx, ic.call, req, true)
				}
				r.observeToolResults(results)
				return results
			}
		}
	}

	if len(toRun) == 1 {
		ic := toRun[0]
		results[ic.index] = r.executeSingleTool(ctx, ic.call, req, true)
		r.observeToolResults(results)
		return results
	}
	if len(toRun) == 0 {
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

const defaultMaxContextChars = 120_000
const toolResultCleared = "[previous result cleared to save context — key findings should already be incorporated in later messages]"

// compressContext replaces old tool result bodies with a short stub when total
// message size exceeds the budget. It preserves the most recent tool results
// (last 8 messages) and all non-tool messages intact.
func (r Runner) compressContext(messages []llm.Message) []llm.Message {
	limit := r.MaxContextChars
	if limit <= 0 {
		limit = defaultMaxContextChars
	}
	total := 0
	for i := range messages {
		total += len(messages[i].Content)
	}
	if total <= limit {
		return messages
	}

	// Find the boundary: preserve the last 8 messages unconditionally.
	preserve := 8
	if preserve > len(messages) {
		preserve = len(messages)
	}
	boundary := len(messages) - preserve

	for i := range messages[:boundary] {
		if messages[i].Role == "tool" && len(messages[i].Content) > len(toolResultCleared) {
			total -= len(messages[i].Content)
			messages[i].Content = toolResultCleared
			total += len(toolResultCleared)
			if total <= limit {
				break
			}
		}
	}
	return messages
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

// streamGuard buffers initial tokens and suppresses downstream delivery if
// the content looks like a textual tool call (e.g. <tool_invocation ...>).
type streamGuard struct {
	downstream llm.StreamCallback
	buf        strings.Builder
	flushed    bool
	suppressed bool
}

const streamGuardThreshold = 120

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

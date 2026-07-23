package agent

import (
	"strings"

	"github.com/noknov/slack-copilot-agent/internal/llm"
)

// streamRouter buffers ambiguous streamed text until the turn shape is known.
// Text before tool calls is narration; text from a no-tool final turn is held
// until the final-answer validators approve it.
type streamRouter struct {
	emit          func(StreamEvent)
	afterTools    bool
	buf           strings.Builder
	toolTurn      bool
	answerFlushed bool
	pendingAnswer string
}

func (sr *streamRouter) text(delta string) {
	if delta != "" {
		sr.buf.WriteString(delta)
	}
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

func (sr *streamRouter) commitAnswer(validated string) {
	text := strings.TrimSpace(validated)
	if text == "" {
		text = sr.pendingAnswer
	}
	if text == "" {
		return
	}
	sr.pendingAnswer = ""
	sr.answerFlushed = true
	sr.emit(StreamEvent{Kind: StreamAnswer, Delta: text})
}

func (sr *streamRouter) discardAnswer() {
	sr.pendingAnswer = ""
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
		sr.pendingAnswer = text
		return
	}
	sr.emit(StreamEvent{Kind: kind, Delta: text})
}

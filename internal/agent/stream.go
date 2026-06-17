package agent

import "strings"

// StreamKind classifies agent output for downstream delivery.
type StreamKind int

const (
	StreamNarration StreamKind = iota
	StreamAnswer
)

// StreamEvent is a typed chunk emitted while the agent runs.
type StreamEvent struct {
	Kind  StreamKind
	Delta string
}

// turnStreamRouter buffers ambiguous tool-turn text until tool calls start or
// the turn ends, then emits narration or answer events.
type turnStreamRouter struct {
	emit           func(StreamEvent)
	toolsInRequest bool
	pendingSep     bool
	buf            strings.Builder
	toolTurn       bool
	streamed       *bool
}

func newTurnStreamRouter(toolsInRequest bool, pendingSep bool, emit func(StreamEvent), streamed *bool) *turnStreamRouter {
	if emit == nil {
		return nil
	}
	return &turnStreamRouter{
		emit:           emit,
		toolsInRequest: toolsInRequest,
		pendingSep:     pendingSep,
		streamed:       streamed,
	}
}

func (tr *turnStreamRouter) onText(delta string) {
	if delta == "" {
		return
	}
	if tr.streamed != nil {
		*tr.streamed = true
	}
	if !tr.toolsInRequest {
		tr.emitAnswer(delta)
		return
	}
	if tr.toolTurn {
		tr.emitNarration(delta)
		return
	}
	tr.buf.WriteString(delta)
}

func (tr *turnStreamRouter) onToolCallsStarted() {
	if tr.toolTurn || !tr.toolsInRequest {
		return
	}
	tr.toolTurn = true
	tr.flushBufAsNarration()
}

func (tr *turnStreamRouter) finishTurn(hasToolCalls bool) {
	if !tr.toolsInRequest {
		return
	}
	if tr.toolTurn || hasToolCalls {
		if !tr.toolTurn {
			tr.flushBufAsNarration()
		}
		return
	}
	tr.flushBufAsAnswer()
}

func (tr *turnStreamRouter) emitNonStreamNarration(text string) {
	text = strings.TrimSpace(text)
	if text == "" || !tr.toolsInRequest {
		return
	}
	tr.emitNarration(text)
}

func (tr *turnStreamRouter) flushBufAsNarration() {
	if tr.buf.Len() == 0 {
		return
	}
	tr.emitNarration(tr.buf.String())
	tr.buf.Reset()
}

func (tr *turnStreamRouter) flushBufAsAnswer() {
	if tr.buf.Len() == 0 {
		return
	}
	tr.emitAnswer(tr.buf.String())
	tr.buf.Reset()
}

func (tr *turnStreamRouter) emitNarration(delta string) {
	if delta == "" {
		return
	}
	if tr.pendingSep {
		delta = "\n\n" + delta
		tr.pendingSep = false
	}
	tr.emit(StreamEvent{Kind: StreamNarration, Delta: delta})
}

func (tr *turnStreamRouter) emitAnswer(delta string) {
	if delta == "" {
		return
	}
	tr.emit(StreamEvent{Kind: StreamAnswer, Delta: delta})
}

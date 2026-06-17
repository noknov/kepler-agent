package agent

// StreamKind classifies a streaming text delta so the delivery layer can route
// it to the correct UI message without any heuristic guessing.
type StreamKind int

const (
	StreamNarration StreamKind = iota // progress text before/between tool calls
	StreamAnswer                      // final answer text (no more tool calls)
)

// StreamEvent is emitted by Runner.OnStream during a live LLM response.
type StreamEvent struct {
	Kind  StreamKind
	Delta string
}

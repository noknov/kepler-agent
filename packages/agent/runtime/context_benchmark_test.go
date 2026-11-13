package runtime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// This measures harness projection, without provider/network latency or an LLM
// interpreting a benchmark-specific prompt. The same fixture runs on the base.
func BenchmarkLongSessionProjection(b *testing.B) {
	for _, turns := range []int{10, 50, 500} {
		b.Run(fmt.Sprintf("turns_%d", turns), func(b *testing.B) {
			var events []transcript.Event
			for turn := 0; turn < turns; turn++ {
				id := fmt.Sprintf("turn-%d", turn)
				input := model.TextMessage(model.RoleUser, "Inspect repository evidence and preserve the user's constraints.")
				callID := fmt.Sprintf("call-%d", turn)
				call := model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: callID, Name: "code-read_file", Arguments: []byte(`{"path":"main.go"}`)}}}}
				events = append(events,
					transcript.Event{Sequence: uint64(len(events) + 1), TurnID: id, Type: transcript.UserInput, Message: &input},
					transcript.Event{Sequence: uint64(len(events) + 2), TurnID: id, Type: transcript.AssistantMessage, Message: &call},
					toolResultEvent(id, uint64(len(events)+3), callID, strings.Repeat("source evidence\n", 500)),
				)
			}
			projector := NewBoundedProjector(ContextConfig{MaxTokens: 32000, ReserveTokens: 4000, OutputTokens: 4000})
			request := ProjectRequest{Events: events}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := projector.Project(b.Context(), request); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

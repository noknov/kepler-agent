package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// The benchmark adapter emits canonical events, without a terminal UI or a
// second execution protocol. Text mode prints only the final answer.
type eventRenderer struct {
	mode   string
	stdout io.Writer
	mu     sync.Mutex
	err    error
}

func newEventRenderer(mode string, stdout, _ io.Writer) *eventRenderer {
	return &eventRenderer{mode: mode, stdout: stdout}
}

func (r *eventRenderer) Publish(_ context.Context, event transcript.Event) {
	if r.mode != "jsonl" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = json.NewEncoder(r.stdout).Encode(event)
	}
}

func (r *eventRenderer) finish(result agentruntime.TurnResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mode == "text" && r.err == nil {
		_, r.err = fmt.Fprintln(r.stdout, result.Message.Text())
	}
}

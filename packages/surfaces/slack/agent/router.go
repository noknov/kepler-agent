package slackagent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"go.opentelemetry.io/otel/attribute"
)

type eventRouter struct {
	mu      sync.RWMutex
	streams map[string]*slackStream
}

func (r *eventRouter) Publish(_ context.Context, event transcript.Event) {
	r.mu.RLock()
	stream := r.streams[event.TurnID]
	r.mu.RUnlock()
	if stream == nil {
		return
	}
	if event.Type == transcript.ContextProjected && stream.telemetry != nil {
		var projection struct {
			PromptHash string `json:"prompt_hash"`
		}
		if json.Unmarshal(event.Metadata, &projection) == nil && projection.PromptHash != "" {
			stream.telemetry.span.SetAttributes(attribute.String("langfuse.version", projection.PromptHash), attribute.String("langfuse.observation.metadata.prompt_hash", projection.PromptHash))
		}
	}
	stream.Lifecycle(event)
	if event.Type == transcript.ModelStreamed && event.Model != nil {
		switch event.Model.Type {
		case model.StreamTextDelta:
			stream.AppendDelta(event.Model.Text)
		}
	}
}

func (r *eventRouter) set(turnID string, stream *slackStream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stream == nil {
		delete(r.streams, turnID)
	} else {
		r.streams[turnID] = stream
	}
}

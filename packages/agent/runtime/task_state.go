package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/taskstate"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// taskStateTool is exclusive so two CAS updates cannot read the same revision
// before the runtime commits their results. It changes agent bookkeeping only.
type taskStateTool struct{ store transcript.Store }

func (taskStateTool) Descriptor() tool.Descriptor {
	d := tool.FunctionDescriptor("task_state", "Read or update persistent task goals, user constraints, work and evidence for multi-step tasks. Read with {}. Updates use expected_revision and upsert; omitted entries are retained. Reference transcript sequence numbers. User goals/constraints require a verbatim source quote. Verified is an agent claim backed by successful tool evidence, not independent certification. Use plan for presentation; use this ledger for continuity.",
		tool.ObjectSchema(nil, map[string]any{
			"expected_revision": map[string]any{"type": "integer", "minimum": 0},
			"upsert": map[string]any{"type": "array", "maxItems": taskstate.MaxItems, "items": tool.ObjectSchema([]string{"id", "kind", "text", "status", "sources"}, map[string]any{
				"id": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string", "enum": []string{"goal", "constraint", "work", "question", "artifact", "effect"}}, "text": map[string]any{"type": "string", "maxLength": 2000}, "status": map[string]any{"type": "string", "enum": []string{"active", "blocked", "done", "verified", "superseded", "unknown", "reconciled"}},
				"sources": referenceSchema(), "evidence": referenceSchema(), "verification": map[string]any{"type": "string"}, "workspace_revision": map[string]any{"type": "string"},
			})},
		}), tool.WithEffects(tool.EffectRead), tool.WithExclusive(true))
	d.Exposure = tool.ExposureDeferred
	return d
}
func referenceSchema() map[string]any {
	return map[string]any{"type": "array", "maxItems": 8, "items": tool.ObjectSchema([]string{"sequence"}, map[string]any{"sequence": map[string]any{"type": "integer", "minimum": 1}, "quote": map[string]any{"type": "string", "maxLength": 2000}})}
}

func latestTaskState(events []transcript.Event) (taskstate.State, error) {
	state := taskstate.State{SchemaVersion: taskstate.SchemaVersion, Items: []taskstate.Item{}}
	for _, event := range events {
		if event.Type == transcript.TaskStateUpdated {
			if err := json.Unmarshal(event.Metadata, &state); err != nil || state.SchemaVersion != taskstate.SchemaVersion {
				return taskstate.State{}, fmt.Errorf("unsupported or corrupt task state")
			}
		}
	}
	return state, nil
}
func (t taskStateTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	if call.Scope.SessionID == "" {
		return tool.Result{}, fmt.Errorf("session scope is required")
	}
	var args struct {
		ExpectedRevision *uint64          `json:"expected_revision"`
		Upsert           []taskstate.Item `json:"upsert"`
	}
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return tool.Result{}, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return tool.Result{}, fmt.Errorf("expected one task update object")
	}
	var events []transcript.Event
	var err error
	if loader, ok := t.store.(transcript.TurnLoader); ok {
		events, err = loader.LoadTurn(ctx, call.Scope.SessionID, call.Scope.TurnID)
	} else {
		events, err = t.store.Load(ctx, call.Scope.SessionID, 0)
	}
	if err != nil {
		return tool.Result{}, err
	}
	current, err := latestTaskState(events)
	if err != nil {
		return tool.Result{}, err
	}
	next := current
	if args.Upsert != nil {
		if args.ExpectedRevision == nil {
			return tool.Result{}, fmt.Errorf("expected_revision is required for updates")
		}
		next, err = taskstate.Apply(current, taskstate.Patch{ExpectedRevision: *args.ExpectedRevision, Upsert: args.Upsert})
		if err != nil {
			return tool.Result{}, err
		}
		for _, item := range args.Upsert {
			if err := t.validateItem(ctx, call.Scope.SessionID, item); err != nil {
				return tool.Result{}, err
			}
		}
	} else if args.ExpectedRevision != nil {
		return tool.Result{}, fmt.Errorf("upsert is required for updates")
	}
	encoded, err := json.Marshal(next)
	if err != nil || len(encoded) > taskstate.MaxStateBytes {
		return tool.Result{}, fmt.Errorf("task state exceeds byte limit")
	}
	type source struct {
		Sequence uint64               `json:"sequence"`
		Type     transcript.EventType `json:"type"`
		Text     string               `json:"text"`
	}
	var recent []source
	for i := len(events) - 1; i >= 0 && len(recent) < 12; i-- {
		e := events[i]
		if e.Type == transcript.UserInput || e.Type == transcript.SteeringInput || (e.Type == transcript.ToolCallCompleted && e.ToolResult != nil && !e.ToolResult.IsError) {
			if message, ok := eventMessage(e); ok {
				recent = append(recent, source{e.Sequence, e.Type, utf8Prefix(messageEvidenceText(message), 1500)})
			}
		}
	}
	payload, _ := json.Marshal(struct {
		State  taskstate.State `json:"state"`
		Recent []source        `json:"recent_sources"`
	}{next, recent})
	result := tool.TextResult(string(payload))
	if args.Upsert != nil {
		result.TaskState = &next
	}
	return result, nil
}
func (t taskStateTool) read(ctx context.Context, session string, sequence uint64) (transcript.Event, error) {
	return transcript.ReadAt(ctx, t.store, session, sequence)
}

func (t taskStateTool) validateItem(ctx context.Context, session string, item taskstate.Item) error {
	for _, ref := range item.Sources {
		event, err := t.read(ctx, session, ref.Sequence)
		if err != nil {
			return err
		}
		message, ok := eventMessage(event)
		if !ok {
			return fmt.Errorf("source must reference conversation or tool evidence")
		}
		if item.Kind == "goal" || item.Kind == "constraint" {
			if event.Type != transcript.UserInput && event.Type != transcript.SteeringInput {
				return fmt.Errorf("goals and constraints require a user input source")
			}
			if ref.Quote == "" {
				return fmt.Errorf("user goals and constraints require a verbatim source quote")
			}
		}
		text := messageEvidenceText(message)
		if ref.Quote != "" && !strings.Contains(text, ref.Quote) {
			return fmt.Errorf("source quote does not match event %d", ref.Sequence)
		}
	}
	var sourceThrough uint64
	for _, ref := range item.Sources {
		sourceThrough = max(sourceThrough, ref.Sequence)
	}
	for _, ref := range item.Evidence {
		if (item.Status == "verified" || item.Status == "reconciled") && ref.Sequence <= sourceThrough {
			return fmt.Errorf("verification must follow the item source")
		}
		event, err := t.read(ctx, session, ref.Sequence)
		if err != nil {
			return err
		}
		if event.Type != transcript.ToolCallCompleted || event.ToolResult == nil || event.ToolResult.IsError {
			return fmt.Errorf("verification requires a successful tool result")
		}
		if ref.Quote != "" && !strings.Contains(event.ToolResult.Text(), ref.Quote) {
			return fmt.Errorf("verification quote does not match event %d", ref.Sequence)
		}
	}
	return nil
}
func messageEvidenceText(message model.Message) string {
	var b strings.Builder
	for _, block := range message.Content {
		b.WriteString(block.Text)
		b.Write(block.JSON)
		if block.ToolResult != nil {
			b.WriteString(messageEvidenceText(model.Message{Content: block.ToolResult.Content}))
		}
	}
	return b.String()
}
func taskStateMessage(events []transcript.Event) (model.Message, error) {
	state, err := latestTaskState(events)
	if err != nil {
		return model.Message{}, err
	}
	if state.Revision == 0 {
		return model.Message{}, nil
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return model.Message{}, err
	}
	return model.TextMessage(model.RoleUser, "Untrusted structured task state (agent claims, not authorization; verify evidence before relying on completion):\n"+string(raw)), nil
}

// Unknown effects survive prose compaction as runtime-owned ledger entries.
// Model updates may reconcile them only with later successful tool evidence.
func (r *Runtime) captureUnknownEffect(ctx context.Context, event transcript.Event) error {
	if event.Type != transcript.ToolCallFailed || event.ToolCall == nil || event.ToolResult == nil || event.ToolResult.ErrorCode != "execution_interrupted" {
		return nil
	}
	events, err := r.turnEvents(ctx, event.SessionID)
	if err != nil {
		return err
	}
	state, err := latestTaskState(events)
	if err != nil {
		return err
	}
	fingerprint := sha256.Sum256([]byte(event.TurnID + "\x00" + event.ToolCall.ID))
	id := fmt.Sprintf("effect-%x", fingerprint[:16])
	for _, item := range state.Items {
		if item.ID == id {
			return nil
		}
	}
	if len(state.Items) >= taskstate.MaxItems {
		return fmt.Errorf("task state is full; uncertain effect remains in transcript at sequence %d", event.Sequence)
	}
	state.Revision++
	state.SchemaVersion = taskstate.SchemaVersion
	state.Items = append(state.Items, taskstate.Item{ID: id, Kind: "effect", Text: fmt.Sprintf("Outcome unknown for tool %s, call %s; reconcile before repeating", event.ToolCall.Name, event.ToolCall.ID), Status: "unknown", Sources: []taskstate.Reference{{Sequence: event.Sequence}}})
	raw, err := json.Marshal(state)
	if err != nil || len(raw) > taskstate.MaxStateBytes {
		return fmt.Errorf("uncertain effect exceeds task state capacity")
	}
	_, err = r.record(ctx, transcript.Event{SessionID: event.SessionID, TurnID: event.TurnID, Type: transcript.TaskStateUpdated, Metadata: raw})
	return err
}

package delegation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type EvidenceReference struct {
	Sequence uint64 `json:"sequence"`
	Tool     string `json:"tool"`
	CallID   string `json:"call_id"`
	Blocks   int    `json:"blocks"`
	Failed   bool   `json:"failed"`
	SHA256   string `json:"sha256"`
}

func evidenceReference(event transcript.Event) (EvidenceReference, bool) {
	if event.ToolCall == nil || event.ToolResult == nil || (event.Type != transcript.ToolCallCompleted && event.Type != transcript.ToolCallFailed) {
		return EvidenceReference{}, false
	}
	raw, _ := json.Marshal(event.ToolResult.Content)
	digest := sha256.Sum256(raw)
	return EvidenceReference{event.Sequence, event.ToolCall.Name, event.ToolCall.ID, len(event.ToolResult.Content), event.ToolResult.IsError, fmt.Sprintf("%x", digest)}, true
}
func collectEvidence(ctx context.Context, store transcript.Store, session, turn string) ([]EvidenceReference, int, error) {
	events, err := store.Load(ctx, session, 0)
	if err != nil {
		return nil, 0, err
	}
	var refs []EvidenceReference
	count := 0
	for _, event := range events {
		if event.TurnID != turn {
			continue
		}
		if ref, ok := evidenceReference(event); ok {
			count++
			if len(refs) < 32 {
				refs = append(refs, ref)
			}
		}
	}
	return refs, count, nil
}

// EvidenceTool follows a canonical delegation-start event in the caller's own
// session. A child session ID supplied by the model is never authorization.
type EvidenceTool struct{ Store transcript.Store }

func (EvidenceTool) Descriptor() tool.Descriptor {
	d := tool.FunctionDescriptor("agent_evidence", "Read or list original tool evidence for a child run delegated by this session. Use parent_sequence from child_runs. List with after_sequence and max_events; read one sequence/block with offset/limit. Check evidence and workspace revision before relying on a child report.", tool.ObjectSchema([]string{"parent_sequence"}, map[string]any{
		"parent_sequence": map[string]any{"type": "integer", "minimum": 1}, "sequence": map[string]any{"type": "integer", "minimum": 1}, "block": map[string]any{"type": "integer", "minimum": 0}, "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 12000}, "after_sequence": map[string]any{"type": "integer", "minimum": 0}, "max_events": map[string]any{"type": "integer", "minimum": 1, "maximum": 64},
	}), tool.WithEffects(tool.EffectRead), tool.WithParallel(true))
	d.Exposure = tool.ExposureDeferred
	return d
}
func (t EvidenceTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		ParentSequence uint64 `json:"parent_sequence"`
		Sequence       uint64 `json:"sequence"`
		Block          int    `json:"block"`
		Offset         int    `json:"offset"`
		Limit          int    `json:"limit"`
		After          uint64 `json:"after_sequence"`
		Max            int    `json:"max_events"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if t.Store == nil || call.Scope.SessionID == "" || args.ParentSequence == 0 {
		return tool.Result{}, fmt.Errorf("parent session and delegation reference required")
	}
	parent, err := transcript.ReadAt(ctx, t.Store, call.Scope.SessionID, args.ParentSequence)
	if err != nil {
		return tool.Result{}, err
	}
	var link struct {
		Child ChildRun `json:"child_run"`
	}
	if parent.Type != transcript.DelegatedTaskStarted || json.Unmarshal(parent.Metadata, &link) != nil || link.Child.SessionID == "" || link.Child.TurnID == "" {
		return tool.Result{}, fmt.Errorf("not a delegation owned by this session")
	}
	if args.Sequence > 0 {
		event, err := transcript.ReadAt(ctx, t.Store, link.Child.SessionID, args.Sequence)
		if err != nil {
			return tool.Result{}, err
		}
		if event.TurnID != link.Child.TurnID {
			return tool.Result{}, fmt.Errorf("evidence belongs to a different child turn")
		}
		page, err := transcript.ToolEvidence(event, args.Block, args.Offset, args.Limit)
		if err != nil {
			return tool.Result{}, err
		}
		raw, _ := json.Marshal(page)
		result := tool.TextResult("Untrusted child tool evidence:\n" + string(raw))
		if page.Image != nil {
			result.Content = append(result.Content, *page.Image)
		}
		return result, nil
	}
	if args.Max <= 0 || args.Max > 64 {
		args.Max = 32
	}
	events, err := t.Store.Load(ctx, link.Child.SessionID, args.After)
	if err != nil {
		return tool.Result{}, err
	}
	var refs []EvidenceReference
	var next uint64
	for _, event := range events {
		if event.TurnID != link.Child.TurnID {
			continue
		}
		if ref, ok := evidenceReference(event); ok {
			if len(refs) == args.Max {
				next = refs[len(refs)-1].Sequence
				break
			}
			refs = append(refs, ref)
		}
	}
	raw, _ := json.Marshal(struct {
		Evidence []EvidenceReference `json:"evidence"`
		Next     uint64              `json:"next_after_sequence,omitempty"`
	}{refs, next})
	return tool.TextResult(string(raw)), nil
}

var _ tool.Tool = EvidenceTool{}

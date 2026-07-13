package memory

import "github.com/wati/oncall-agent/internal/llm"

// ContentReplacementRecord is the persisted form of a model-visible content
// replacement decision. It mirrors Claude Code's transcript record: store the
// exact replacement string so resume never regenerates a different preview.
type ContentReplacementRecord struct {
	Kind        string `json:"kind"`
	ToolCallID  string `json:"tool_call_id"`
	Replacement string `json:"replacement"`
}

const ContentReplacementKindToolResult = "tool-result"

// ContentReplacementState tracks aggregate tool-result budget decisions for a
// conversation. Once a tool result is seen, its fate is frozen:
// replaced results always re-use the exact same replacement, and unreplaced
// results are never replaced later.
type ContentReplacementState struct {
	Seen         map[string]bool
	Replacements map[string]string
	Records      []ContentReplacementRecord
}

func NewContentReplacementState() *ContentReplacementState {
	return &ContentReplacementState{
		Seen:         map[string]bool{},
		Replacements: map[string]string{},
	}
}

func ReconstructContentReplacementState(messages []llm.Message, records []ContentReplacementRecord) *ContentReplacementState {
	state := NewContentReplacementState()
	for _, msg := range messages {
		if msg.Role == "tool" && msg.ToolCallID != "" {
			state.Seen[msg.ToolCallID] = true
		}
	}
	for _, record := range records {
		if record.Kind != ContentReplacementKindToolResult || record.ToolCallID == "" {
			continue
		}
		if len(state.Seen) > 0 && !state.Seen[record.ToolCallID] {
			continue
		}
		state.Replacements[record.ToolCallID] = record.Replacement
	}
	state.Records = append(state.Records, records...)
	return state
}

func (s *ContentReplacementState) AddReplacement(toolCallID, replacement string) {
	if s == nil || toolCallID == "" || replacement == "" {
		return
	}
	if s.Seen == nil {
		s.Seen = map[string]bool{}
	}
	if s.Replacements == nil {
		s.Replacements = map[string]string{}
	}
	s.Seen[toolCallID] = true
	s.Replacements[toolCallID] = replacement
	for _, record := range s.Records {
		if record.Kind == ContentReplacementKindToolResult && record.ToolCallID == toolCallID {
			return
		}
	}
	s.Records = append(s.Records, ContentReplacementRecord{
		Kind:        ContentReplacementKindToolResult,
		ToolCallID:  toolCallID,
		Replacement: replacement,
	})
}

func (s *ContentReplacementState) MarkSeen(toolCallID string) {
	if s == nil || toolCallID == "" {
		return
	}
	if s.Seen == nil {
		s.Seen = map[string]bool{}
	}
	s.Seen[toolCallID] = true
}

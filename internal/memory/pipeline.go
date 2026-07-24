package memory

import (
	"context"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/internal/llm"
)

var timeNowUTC = func() time.Time { return time.Now().UTC() }

// Pipeline is the single entry point for request-time memory management.
//
// The boundaries are intentionally explicit:
//   - Slack thread snapshots are transient external evidence, equivalent to a
//     tool result fetched at the start of each run. They are injected verbatim
//     and are not summarized into conversation memory.
//   - Session summary is the LLM-produced medium-term memory for older agent
//     conversation.
//   - Turns are short-term agent conversation history. They may only be reduced
//     by LLM summary, except tool results which can be locally shed.
//   - Tool results are evidence blobs. They may be spilled, cleared, or
//     head/tail-compressed because the model can request focused slices again.
type Pipeline struct {
	Builder   Builder
	Compactor *Compactor
}

type ActiveRequestInput struct {
	SystemPrompt      string
	CompactBoundaries []CompactBoundary
	ExternalEvidence  []ExternalEvidence
	UserText          string
	UserParts         []llm.ContentPart
	SessionSummary    string
	Turns             []Turn
}

type ActiveRequest struct {
	Messages               []llm.Message
	EstimatedTokens        int
	TokenBreakdown         TokenBreakdown
	ExternalEvidenceTokens int
	ConversationTokens     int
}

type TokenBreakdown struct {
	ExternalEvidence int `json:"external_evidence"`
	SessionSummary   int `json:"session_summary"`
	Turns            int `json:"turns"`
	ToolResults      int `json:"tool_results"`
	CompactBoundary  int `json:"compact_boundary"`
	ToolSchemas      int `json:"tool_schemas"`
	Total            int `json:"total"`
}

type SessionCompactResult struct {
	Turns      []Turn
	Summary    string
	Compressed bool
	Layer      string
	PreTokens  int
	PostTokens int
}

func NewPipeline(builder Builder, compactor *Compactor) *Pipeline {
	return &Pipeline{Builder: builder, Compactor: compactor}
}

// BuildActiveRequest creates the exact message window for a model run. External
// evidence is deliberately injected as-is; no compression decision is made here
// because these snapshots belong to their source system, not to the agent
// transcript.
func (p *Pipeline) BuildActiveRequest(input ActiveRequestInput) ActiveRequest {
	builder := p.builder()
	messages := builder.BuildRequest(BuildRequest{
		SystemPrompt:      input.SystemPrompt,
		CompactBoundaries: input.CompactBoundaries,
		ExternalEvidence:  input.ExternalEvidence,
		UserText:          input.UserText,
		UserParts:         input.UserParts,
		Summary:           input.SessionSummary,
		Turns:             input.Turns,
	})
	estimatedTokens := CountTokensWithCalibration(messages)
	persistentTurns := FilterPersistentTurns(input.Turns)
	return ActiveRequest{
		Messages:               messages,
		EstimatedTokens:        estimatedTokens,
		TokenBreakdown:         buildTokenBreakdownCached(input, messages, estimatedTokens, persistentTurns),
		ExternalEvidenceTokens: estimateExternalEvidenceTokens(input.ExternalEvidence),
		ConversationTokens:     CountTokensWithCalibration(ToLLM(persistentTurns)),
	}
}

func (b TokenBreakdown) WithToolSchemas(tokens int) TokenBreakdown {
	b.ToolSchemas = tokens
	b.Total += tokens
	return b
}

func (b TokenBreakdown) Metadata() map[string]any {
	return map[string]any{
		"external_evidence_tokens": b.ExternalEvidence,
		"session_summary_tokens":   b.SessionSummary,
		"turn_tokens":              b.Turns,
		"tool_result_tokens":       b.ToolResults,
		"compact_boundary_tokens":  b.CompactBoundary,
		"tool_schema_tokens":       b.ToolSchemas,
		"total_tokens":             b.Total,
	}
}

func NewCompactBoundary(layer, summary string, preTokens, postTokens int) CompactBoundary {
	now := timeNowUTC()
	return CompactBoundary{
		ID:         now.Format("20060102T150405.000000000Z"),
		Layer:      strings.TrimSpace(layer),
		Summary:    strings.TrimSpace(summary),
		PreTokens:  preTokens,
		PostTokens: postTokens,
		CreatedAt:  now,
	}
}

// CompactSessionConversation compacts only persisted agent conversation. It
// never receives Slack thread context, so thread evidence cannot be silently
// folded into a session summary.
func (p *Pipeline) CompactSessionConversation(ctx context.Context, turns []Turn, existingSummary string) SessionCompactResult {
	out := SessionCompactResult{
		Turns:   FilterPersistentTurns(turns),
		Summary: strings.TrimSpace(existingSummary),
	}
	if p == nil || p.Compactor == nil || len(out.Turns) == 0 {
		return out
	}

	preTurnCount := len(out.Turns)
	llmMessages := ToLLM(out.Turns)
	compacted, result, err := p.Compactor.CompactIfNeeded(ctx, llmMessages)
	if err != nil || result == nil {
		return out
	}

	out.Layer = result.Layer
	out.PreTokens = result.PreTokens
	out.PostTokens = result.PostTokens
	if result.Layer != "" {
		out.Turns = FilterPersistentTurns(FromLLM(compacted))
		out.Compressed = result.PostTokens < result.PreTokens || result.Summary != "" || len(out.Turns) < preTurnCount
		if result.Layer == "llm_compact" && strings.TrimSpace(result.Summary) != "" {
			out.Summary = strings.TrimSpace(result.Summary)
		}
	}
	return out
}

func (p *Pipeline) builder() Builder {
	if p == nil {
		return Builder{}
	}
	return p.Builder
}

func estimateExternalEvidenceTokens(items []ExternalEvidence) int {
	total := 0
	for _, item := range items {
		total += RoughTokenEstimate(item.Content)
	}
	return total
}

func buildTokenBreakdown(input ActiveRequestInput, messages []llm.Message) TokenBreakdown {
	return buildTokenBreakdownCached(input, messages, CountTokensWithCalibration(messages), FilterPersistentTurns(input.Turns))
}

func buildTokenBreakdownCached(input ActiveRequestInput, messages []llm.Message, totalTokens int, persistentTurns []Turn) TokenBreakdown {
	breakdown := TokenBreakdown{
		ExternalEvidence: estimateExternalEvidenceTokens(input.ExternalEvidence),
		SessionSummary:   RoughTokenEstimate(input.SessionSummary),
		CompactBoundary:  estimateCompactBoundaryTokens(input.CompactBoundaries),
	}
	for _, turn := range persistentTurns {
		tokens := estimateMessageTokens(&llm.Message{
			Role:       string(turn.Role),
			Content:    turn.Content,
			Name:       turn.Name,
			ToolCallID: turn.ToolCallID,
		})
		if turn.Role == RoleTool {
			breakdown.ToolResults += tokens
		} else {
			breakdown.Turns += tokens
		}
	}
	breakdown.Total = totalTokens
	return breakdown
}

func estimateCompactBoundaryTokens(boundaries []CompactBoundary) int {
	if len(boundaries) == 0 {
		return 0
	}
	latest := boundaries[len(boundaries)-1]
	return RoughTokenEstimate(latest.ID) + RoughTokenEstimate(latest.Layer) + 30
}

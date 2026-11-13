package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

// A usage anchor describes one successful, unchanged request prefix. It is
// derived evidence, not a global tokenizer correction or an authority token.
// Hashes prevent restored measurements from surviving prompt/schema/route edits.
type contextUsageAnchor struct {
	RouteKey     string `json:"route_key"`
	RequestKey   string `json:"request_key"`
	PrefixKey    string `json:"prefix_key"`
	MessageCount int    `json:"message_count"`
	InputTokens  int64  `json:"input_tokens"`
}

func contextRequestKey(route string, request model.Request) string {
	request.Messages = nil
	request.Metadata = nil
	request.InputTokenEstimate = 0
	return contextDigest(struct {
		Route   string
		Request model.Request
	}{route, request})
}

func contextDigest(value any) string {
	hash := sha256.New()
	if err := json.NewEncoder(hash).Encode(value); err != nil {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (r *Runtime) observeContext(request model.Request, response model.Response) *contextUsageAnchor {
	if response.InputContextKey == "" || (response.FinishReason != model.FinishStop && response.FinishReason != model.FinishToolCalls) {
		return nil
	}
	input := response.Usage.Add(model.Usage{}).InputTokens
	if input <= 0 {
		return nil
	}
	requestKey := contextRequestKey(response.InputContextKey, request)
	prefixKey := contextDigest(request.Messages)
	if requestKey == "" || prefixKey == "" {
		return nil
	}
	return &contextUsageAnchor{RouteKey: response.InputContextKey, RequestKey: requestKey, PrefixKey: prefixKey, MessageCount: len(request.Messages), InputTokens: input}
}

// inputEstimate uses reported input for the exact prefix and estimates only
// new messages. Any edit, compaction, schema/model/route change invalidates it.
func (r *Runtime) inputEstimate(ctx context.Context, turn TurnRequest, messages []model.Message, definitions []model.ToolDefinition) int {
	counter := r.config.Context.counter()
	fallback := func() int { return counter.Messages(messages) + counter.Tools(definitions) }
	r.eventMu.RLock()
	state := r.eventCache[turn.SessionID]
	var anchor *contextUsageAnchor
	if state != nil {
		anchor = state.contextUsage
	}
	r.eventMu.RUnlock()
	if anchor == nil || anchor.MessageCount < 1 || anchor.MessageCount > len(messages) || anchor.InputTokens <= 0 {
		return fallback()
	}
	request := r.modelRequest(turn, nil, definitions)
	routeKey, err := model.InputContextKey(ctx, r.deps.Model, request.Model)
	if err != nil || routeKey == "" || routeKey != anchor.RouteKey {
		return fallback()
	}
	if anchor.RequestKey != contextRequestKey(routeKey, request) || anchor.PrefixKey != contextDigest(messages[:anchor.MessageCount]) {
		return fallback()
	}
	// Counter output is an int throughout projection. Reject a corrupt restored
	// measurement instead of overflowing into a smaller apparent request.
	maxInt := int64(int(^uint(0) >> 1))
	tail := counter.Messages(messages[anchor.MessageCount:])
	if anchor.InputTokens > maxInt-int64(tail) {
		return int(maxInt)
	}
	return int(anchor.InputTokens) + tail
}

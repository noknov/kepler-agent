package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"

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
	digest := contextHash(struct {
		Route   string
		Request model.Request
	}{route, request})
	if digest == nil {
		return ""
	}
	if request.OutputFormat != nil {
		hashContextRaw(digest, request.OutputFormat.Schema)
	}
	for _, definition := range request.Tools {
		hashContextRaw(digest, definition.InputSchema)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// Structural JSON binds field positions. Raw payloads are additionally framed
// byte-for-byte: encoding/json normalizes RawMessage whitespace, but providers
// can render those payloads as text with a different token count.
func contextHash(value any) hash.Hash {
	digest := sha256.New()
	_, _ = digest.Write([]byte("kepler-input-context-v2\x00"))
	if err := json.NewEncoder(digest).Encode(value); err != nil {
		return nil
	}
	return digest
}

func hashContextRaw(digest hash.Hash, raw json.RawMessage) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(raw)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write(raw)
}

func contextMessagesKey(messages []model.Message) string {
	digest := contextHash(messages)
	if digest == nil {
		return ""
	}
	for _, message := range messages {
		hashContextContent(digest, message.Content)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func hashContextContent(digest hash.Hash, content []model.Content) {
	for _, block := range content {
		hashContextRaw(digest, block.JSON)
		if block.ToolCall != nil {
			hashContextRaw(digest, block.ToolCall.Arguments)
		}
		if block.Continuation != nil {
			hashContextRaw(digest, block.Continuation.Items)
		}
		if block.ToolResult != nil {
			hashContextContent(digest, block.ToolResult.Content)
		}
	}
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
	prefixKey := contextMessagesKey(request.Messages)
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
	if anchor.RequestKey != contextRequestKey(routeKey, request) || anchor.PrefixKey != contextMessagesKey(messages[:anchor.MessageCount]) {
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

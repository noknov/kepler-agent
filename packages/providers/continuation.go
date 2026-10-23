package providers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/llm"
	"github.com/noknov/kepler-agent/packages/llm/chatgpt"
)

func (c *Client) continuation(message llm.Message, name, kind string, usage llm.Usage) *model.Continuation {
	if len(message.NativeItems) == 0 || c.route == "" {
		return nil
	}
	raw, err := json.Marshal(message.NativeItems)
	if err != nil {
		return nil
	}
	// Opaque token accounting varies by provider. Retain a conservative encoded
	// size estimate until a provider token counter is configured.
	estimate := (len(raw) + 3) / 4
	if usage.CompletionTokens > estimate {
		estimate = usage.CompletionTokens
	}
	return &model.Continuation{Version: 1, Provider: c.provider, Model: name, Route: c.route, Kind: kind, Items: raw, TokenEstimate: estimate}
}
func (c *Client) inputMessage(message model.Message, name string) (llm.Message, error) {
	if routed, ok := c.Wire.(interface{ AcceptsNativeContinuation(string) bool }); ok && !routed.AcceptsNativeContinuation(name) {
		return toWireMessage(message), nil
	}
	for _, block := range message.Content {
		if block.Type == model.ContentContinuation && block.Continuation != nil {
			state := block.Continuation
			if state.Provider != c.provider || state.Route != c.route || state.Model != name {
				continue
			}
			var items []json.RawMessage
			if state.Version != 1 || (state.Kind != "compaction" && state.Kind != "response") || len(state.Items) > 8<<20 || json.Unmarshal(state.Items, &items) != nil || len(items) == 0 {
				return llm.Message{}, &model.Error{Kind: model.ErrorInvalid, Message: "invalid native continuation window"}
			}
			return llm.Message{Role: string(message.Role), NativeItems: items}, nil
		}
	}
	return toWireMessage(message), nil
}
func (c *Client) compact(ctx context.Context, request model.Request) (model.Response, error) {
	wire, ok := c.Wire.(llm.CompactClient)
	if !ok || c.route == "" {
		return model.Response{}, &model.Error{Kind: model.ErrorUnsupported, Message: "native compaction is unavailable for this route"}
	}
	req := llm.Request{Model: request.Model, Metadata: request.Metadata}
	for _, message := range request.Messages {
		converted, err := c.inputMessage(message, request.Model)
		if err != nil {
			return model.Response{}, err
		}
		req.Messages = append(req.Messages, converted)
	}
	response, err := wire.Compact(ctx, req)
	if err != nil {
		return model.Response{}, toModelError(err)
	}
	state := c.continuation(response.Message, request.Model, "compaction", response.Usage)
	if state == nil {
		return model.Response{}, fmt.Errorf("native compaction did not return a continuation")
	}
	return model.Response{Message: model.Message{Role: model.RoleUser, Content: []model.Content{{Type: model.ContentContinuation, Continuation: state}}}, FinishReason: model.FinishStop, Usage: toUsage(response.Usage)}, nil
}

// A credential path is a storage location, not an account identity. Bind a
// request-local client copy so concurrent requests never mutate shared routing.
func (c *Client) bindSession(ctx context.Context) (*Client, context.Context, error) {
	boundCtx, identity, err := chatgpt.BindIdentity(ctx, c.chatgptPath)
	if err != nil {
		return nil, ctx, err
	}
	bound := *c
	bound.route = fmt.Sprintf("%x", sha256.Sum256([]byte(c.route+":"+identity)))
	return &bound, boundCtx, nil
}

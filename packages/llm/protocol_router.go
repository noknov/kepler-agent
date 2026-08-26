package llm

import (
	"context"
	"strings"
)

// ProtocolRouter routes requests between chat/completions and /responses
// clients based on the requested model name.
type ProtocolRouter struct {
	openAI     Client
	responses  StreamClient
	responsesM map[string]bool
}

func NewProtocolRouter(openAI Client, responses StreamClient, responsesModels []string) *ProtocolRouter {
	models := make(map[string]bool, len(responsesModels))
	for _, model := range responsesModels {
		model = strings.TrimSpace(model)
		if model != "" {
			models[model] = true
		}
	}
	return &ProtocolRouter{
		openAI:     openAI,
		responses:  responses,
		responsesM: models,
	}
}

func (r *ProtocolRouter) usesResponses(model string) bool {
	return r != nil && r.responses != nil && r.responsesM[strings.TrimSpace(model)]
}

func (r *ProtocolRouter) Chat(ctx context.Context, req Request) (Response, error) {
	if r.usesResponses(req.Model) {
		return r.responses.Chat(ctx, req)
	}
	return r.openAI.Chat(ctx, req)
}

func (r *ProtocolRouter) ChatStream(ctx context.Context, req Request, h StreamHandler) (Response, error) {
	if r.usesResponses(req.Model) {
		return r.responses.ChatStream(ctx, req, h)
	}
	stream, ok := r.openAI.(StreamClient)
	if !ok {
		return r.openAI.Chat(ctx, req)
	}
	return stream.ChatStream(ctx, req, h)
}

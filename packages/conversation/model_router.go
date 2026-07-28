package conversation

import "github.com/noknov/slack-copilot-agent/packages/llm"

type ModelRoute struct {
	Model  string
	Reason string
}

type ModelRouter struct {
	DefaultModel            string
	MultimodalFallbackModel string
	SupportsMultimodal      func(model string) bool
}

func (r ModelRouter) Route(req Request) ModelRoute {
	if !hasMultimodalInput(req.ContentParts) {
		return ModelRoute{Model: r.DefaultModel, Reason: "default"}
	}
	if r.supportsMultimodal(r.DefaultModel) {
		return ModelRoute{Model: r.DefaultModel, Reason: "default_multimodal"}
	}
	if r.MultimodalFallbackModel != "" && r.supportsMultimodal(r.MultimodalFallbackModel) {
		return ModelRoute{Model: r.MultimodalFallbackModel, Reason: "multimodal_fallback"}
	}
	if r.MultimodalFallbackModel != "" && r.SupportsMultimodal == nil {
		return ModelRoute{Model: r.MultimodalFallbackModel, Reason: "multimodal_fallback"}
	}
	return ModelRoute{Model: r.DefaultModel, Reason: "default"}
}

func (r ModelRouter) supportsMultimodal(model string) bool {
	return r.SupportsMultimodal != nil && r.SupportsMultimodal(model)
}

func hasMultimodalInput(parts []llm.ContentPart) bool {
	for _, part := range parts {
		if part.Type == "image_url" || part.ImageURL != nil {
			return true
		}
	}
	return false
}

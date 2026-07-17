package conversation

import "github.com/wati/oncall-agent/internal/llm"

type ModelRoute struct {
	Model  string
	Reason string
}

type ModelRouter struct {
	DefaultModel    string
	MultimodalModel string
}

func (r ModelRouter) Route(req Request) ModelRoute {
	if r.MultimodalModel != "" && hasMultimodalInput(req.ContentParts) {
		return ModelRoute{Model: r.MultimodalModel, Reason: "multimodal_input"}
	}
	return ModelRoute{Model: r.DefaultModel, Reason: "default"}
}

func hasMultimodalInput(parts []llm.ContentPart) bool {
	for _, part := range parts {
		if part.Type == "image_url" || part.ImageURL != nil {
			return true
		}
	}
	return false
}

package providers

import "strings"

// Modality is an input capability declared by an exact provider/model route.
// It is deliberately provider-neutral so surfaces do not infer capabilities
// from model names or wire protocols.
type Modality string

const (
	ModalityText  Modality = "text"
	ModalityImage Modality = "image"
)

// ModelCapabilities is the provider-owned contract for one exact model ID.
// Known routes are authoritative; unknown routes have no implied capabilities.
type ModelCapabilities struct {
	Provider        string
	ID              string
	Protocol        string
	InputModalities []Modality
}

func (c ModelCapabilities) SupportsInput(modality Modality) bool {
	for _, supported := range c.InputModalities {
		if supported == modality {
			return true
		}
	}
	return false
}

// knownModels is intentionally small and provider-owned. A new model belongs
// here once its endpoint contract is verified; it should not require edits in
// every surface that happens to consume images or tools.
var knownModels = []ModelCapabilities{
	{
		Provider:        "opencode-go",
		ID:              "grok-4.6",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "opencode-go",
		ID:              "gpt-5.6-luna",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "opencode-go",
		ID:              "deepseek-v4.1-flash",
		Protocol:        "openai",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "opencode-go",
		ID:              "glm-5.2",
		Protocol:        "openai",
		InputModalities: []Modality{ModalityText},
	},
}

// ResolveModel returns the exact provider/model declaration. Unknown models
// are not guessed from naming conventions or from their transport protocol.
func ResolveModel(provider, model string) (ModelCapabilities, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	for _, known := range knownModels {
		if known.Provider == provider && known.ID == model {
			return known, true
		}
	}
	return ModelCapabilities{}, false
}

// responsesModels returns catalog-declared Responses routes in stable order.
// Unknown model IDs must be added to the catalog after their endpoint contract
// is verified; runtime configuration cannot silently bypass that contract.
func responsesModels(provider string) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	models := make([]string, 0)
	for _, known := range knownModels {
		if known.Provider != provider || !strings.EqualFold(known.Protocol, "responses") {
			continue
		}
		models = append(models, known.ID)
	}
	return models
}

// SupportsInput uses an exact catalog declaration. Unknown models are treated
// as unsupported until their capabilities are added to the catalog.
func SupportsInput(provider, model string, modality Modality) bool {
	if known, ok := ResolveModel(provider, model); ok {
		return known.SupportsInput(modality)
	}
	return false
}

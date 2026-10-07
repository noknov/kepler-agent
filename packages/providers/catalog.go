package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/noknov/kepler-agent/packages/llm"
)

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
	{
		Provider:        "deepseek",
		ID:              "deepseek-flash",
		Protocol:        "openai",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "deepseek",
		ID:              "deepseek-v4-flash",
		Protocol:        "openai",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "deepseek",
		ID:              "deepseek-v4-flash-vision-exp",
		Protocol:        "openai",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "deepseek",
		ID:              "deepseek-v4-pro",
		Protocol:        "openai",
		InputModalities: []Modality{ModalityText},
	},
	{
		Provider:        "deepseek",
		ID:              "deepseek-chat",
		Protocol:        "openai",
		InputModalities: []Modality{ModalityText},
	},
	{
		Provider:        "deepseek",
		ID:              "deepseek-reasoner",
		Protocol:        "openai",
		InputModalities: []Modality{ModalityText},
	},
	{
		Provider:        "chatgpt",
		ID:              "gpt-6.1-sol",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "chatgpt",
		ID:              "gpt-6-astra",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "chatgpt",
		ID:              "gpt-6-sol",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "chatgpt",
		ID:              "gpt-6-luna",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "chatgpt",
		ID:              "gpt-5.6-sol",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "chatgpt",
		ID:              "gpt-5.6-terra",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "chatgpt",
		ID:              "gpt-5.6-luna",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "chatgpt",
		ID:              "gpt-5.4",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "chatgpt",
		ID:              "gpt-5.3-codex",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "openai",
		ID:              "gpt-5.4",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "openai",
		ID:              "gpt-5.6-luna",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "openai",
		ID:              "gpt-4.1",
		Protocol:        "responses",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "openai",
		ID:              "gpt-4o",
		Protocol:        "openai",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "anthropic",
		ID:              "claude-opus-4-6",
		Protocol:        "anthropic",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "anthropic",
		ID:              "claude-sonnet-4-6",
		Protocol:        "anthropic",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "anthropic",
		ID:              "claude-sonnet-4-5",
		Protocol:        "anthropic",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "anthropic",
		ID:              "claude-haiku-4-5",
		Protocol:        "anthropic",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "mimo",
		ID:              "mimo-v2.5-pro",
		Protocol:        "openai",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "mimo",
		ID:              "mimo-v2.5",
		Protocol:        "openai",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "opencode-zen",
		ID:              "mimo-v2.5-free",
		Protocol:        "openai",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "kimi",
		ID:              "kimi-k2.5",
		Protocol:        "anthropic",
		InputModalities: []Modality{ModalityText, ModalityImage},
	},
	{
		Provider:        "longcat",
		ID:              "LongCat-2.0",
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

// Capabilities resolves account-owned models from the same cached catalog used
// by inference. Static declarations remain authoritative for other providers.
func (c *Client) Capabilities(ctx context.Context, provider, name string) (ModelCapabilities, error) {
	if qoder, ok := c.Wire.(*llm.QoderClient); ok {
		models, err := qoder.Models(ctx)
		if err != nil {
			return ModelCapabilities{}, err
		}
		for _, item := range models {
			if item.Key != name && item.Name != name {
				continue
			}
			modalities := []Modality{ModalityText}
			if item.Vision {
				modalities = append(modalities, ModalityImage)
			}
			return ModelCapabilities{Provider: "qoder", ID: item.Key, Protocol: "qoder", InputModalities: modalities}, nil
		}
		return ModelCapabilities{}, fmt.Errorf("qoder model %q is not in the enabled account catalog", name)
	}
	capabilities, _ := ResolveModel(provider, name)
	return capabilities, nil
}

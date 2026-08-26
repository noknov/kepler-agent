package tool

import (
	"encoding/json"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/prompts"
)

type DescriptorOption func(*Descriptor)

func WithEffects(effects ...Effect) DescriptorOption {
	return func(d *Descriptor) { d.Effects = append([]Effect(nil), effects...) }
}

func WithExposure(exposure Exposure) DescriptorOption {
	return func(d *Descriptor) { d.Exposure = exposure }
}

func WithParallel(parallel bool) DescriptorOption {
	return func(d *Descriptor) { d.Parallel = parallel }
}

func WithExclusive(exclusive bool) DescriptorOption {
	return func(d *Descriptor) { d.Exclusive = exclusive }
}

func WithTimeout(timeout time.Duration) DescriptorOption {
	return func(d *Descriptor) { d.Timeout = timeout }
}

func WithTags(tags ...string) DescriptorOption {
	return func(d *Descriptor) { d.Tags = append([]string(nil), tags...) }
}

func WithDependencies(deps ...string) DescriptorOption {
	return func(d *Descriptor) { d.Dependencies = append([]string(nil), deps...) }
}

func WithSurfaces(surfaces ...string) DescriptorOption {
	return func(d *Descriptor) { d.Surfaces = append([]string(nil), surfaces...) }
}

// ReadNetworkParallel marks a read-only tool that may call remote APIs and run
// concurrently with other parallel-safe tools.
func ReadNetworkParallel(deps ...string) []DescriptorOption {
	return []DescriptorOption{
		WithEffects(EffectRead, EffectNetwork),
		WithParallel(true),
		WithDependencies(deps...),
	}
}

// NetworkIntegration marks an integration tool that performs network I/O.
func NetworkIntegration(deps ...string) []DescriptorOption {
	return []DescriptorOption{
		WithEffects(EffectNetwork),
		WithDependencies(deps...),
	}
}

// ExternalWrite marks a tool that writes outside the local workspace.
func ExternalWrite(deps ...string) []DescriptorOption {
	return []DescriptorOption{
		WithEffects(EffectExternalWrite, EffectNetwork),
		WithDependencies(deps...),
	}
}

// BindSurface attaches presentation-surface visibility metadata at registration
// time without changing the underlying tool implementation.
func BindSurface(item Tool, surface string, extraDeps ...string) Tool {
	if surface == "" {
		return item
	}
	deps := append([]string{surface}, extraDeps...)
	return Annotate(item, Descriptor{
		Dependencies: deps,
		Surfaces:     []string{surface},
	})
}

// FunctionDescriptor builds a tool descriptor with prompt overlays applied to
// descriptions and parameter help text.
func FunctionDescriptor(name, description string, parameters map[string]any, opts ...DescriptorOption) Descriptor {
	staticDescription := prompts.ToolDescription(name, "")
	description = mergeDescription(staticDescription, description)
	applyParameterPrompts(name, parameters)
	raw, _ := json.Marshal(parameters)
	descriptor := Descriptor{Name: name, Description: description, InputSchema: raw, Effects: []Effect{EffectRead}}
	for _, opt := range opts {
		opt(&descriptor)
	}
	return descriptor
}

func ObjectSchema(required []string, properties map[string]any) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func mergeDescription(staticDescription, dynamicSuffix string) string {
	if staticDescription == "" {
		return dynamicSuffix
	}
	if dynamicSuffix == "" {
		return staticDescription
	}
	return staticDescription + " " + dynamicSuffix
}

func applyParameterPrompts(toolName string, parameters map[string]any) {
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		return
	}
	for param, raw := range properties {
		applyPropertyPrompt(toolName, param, raw)
	}
}

func applyPropertyPrompt(toolName, path string, raw any) {
	property, ok := raw.(map[string]any)
	if !ok {
		return
	}
	current, _ := property["description"].(string)
	if next := prompts.ParamDescription(toolName, path, current); next != "" {
		property["description"] = next
	}
	if nested, ok := property["properties"].(map[string]any); ok {
		for name, child := range nested {
			applyPropertyPrompt(toolName, path+"."+name, child)
		}
	}
	items, ok := property["items"].(map[string]any)
	if !ok {
		return
	}
	current, _ = items["description"].(string)
	if next := prompts.ParamDescription(toolName, path+".items", current); next != "" {
		items["description"] = next
	}
	if nested, ok := items["properties"].(map[string]any); ok {
		for name, child := range nested {
			applyPropertyPrompt(toolName, path+".items."+name, child)
		}
	}
}

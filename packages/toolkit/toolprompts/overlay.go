// Package toolprompts applies product wording outside the canonical agent core.
package toolprompts

import (
	"encoding/json"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/prompts"
)

func Apply(descriptor tool.Descriptor) tool.Descriptor {
	descriptor.Description = mergeDescription(prompts.ToolDescription(descriptor.Name, ""), descriptor.Description)
	var parameters map[string]any
	if json.Unmarshal(descriptor.InputSchema, &parameters) == nil {
		applyParameterPrompts(descriptor.Name, parameters)
		if raw, err := json.Marshal(parameters); err == nil {
			descriptor.InputSchema = raw
		}
	}
	return descriptor
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

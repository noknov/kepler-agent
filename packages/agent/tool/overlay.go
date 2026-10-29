package tool

import (
	"context"
	"encoding/json"
)

// SetPromptOverlay installs product text at composition time. Only description
// and schema wording may change; authorization and scheduling stay authoritative.
func (c *Catalog) SetPromptOverlay(overlay func(Descriptor) Descriptor) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.promptOverlay != nil {
		return ErrAlreadyRegistered
	}
	c.promptOverlay = overlay
	for name, item := range c.tools {
		c.tools[name] = withPromptOverlay(item, overlay)
	}
	c.searchIndexes = nil
	return nil
}

type promptTool struct {
	inner      Tool
	descriptor Descriptor
}

func withPromptOverlay(item Tool, overlay func(Descriptor) Descriptor) Tool {
	if overlay == nil {
		return item
	}
	d := item.Descriptor()
	copyDescriptor := d
	copyDescriptor.Effects = append([]Effect(nil), d.Effects...)
	copyDescriptor.InputSchema = append([]byte(nil), d.InputSchema...)
	copyDescriptor.Tags = append([]string(nil), d.Tags...)
	copyDescriptor.Dependencies = append([]string(nil), d.Dependencies...)
	copyDescriptor.Surfaces = append([]string(nil), d.Surfaces...)
	wording := overlay(copyDescriptor)
	d.Description = wording.Description
	d.InputSchema = wording.InputSchema
	return promptTool{inner: item, descriptor: d}
}
func (t promptTool) Descriptor() Descriptor { return t.descriptor }
func (t promptTool) Execute(ctx context.Context, call Call) (Result, error) {
	return t.inner.Execute(ctx, call)
}
func (t promptTool) PrepareCall(call Call) (json.RawMessage, error) {
	return prepareCallState(t.inner, call)
}
func (t promptTool) DescriptorForCall(call Call) (Descriptor, error) {
	d, err := DescribeCall(t.inner, call)
	if err != nil {
		return Descriptor{}, err
	}
	d.Description = t.descriptor.Description
	d.InputSchema = t.descriptor.InputSchema
	return d, nil
}
func (t promptTool) EndTurn(sessionID, turnID string) {
	if lifecycle, ok := t.inner.(TurnLifecycle); ok {
		lifecycle.EndTurn(sessionID, turnID)
	}
}

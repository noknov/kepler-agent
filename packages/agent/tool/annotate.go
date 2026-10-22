package tool

import "context"

// Annotate wraps a tool and merges descriptor fields from patch into the
// inner descriptor. Patch fields override only when explicitly set.
func Annotate(inner Tool, patch Descriptor) Tool {
	return annotatedTool{inner: inner, patch: patch}
}

type annotatedTool struct {
	inner Tool
	patch Descriptor
}

func (t annotatedTool) Descriptor() Descriptor {
	return mergeDescriptor(t.inner.Descriptor(), t.patch)
}

func (t annotatedTool) DescriptorForCall(call Call) (Descriptor, error) {
	d, err := DescribeCall(t.inner, call)
	if err != nil {
		return Descriptor{}, err
	}
	return mergeDescriptor(d, t.patch), nil
}

func mergeDescriptor(base Descriptor, patch Descriptor) Descriptor {
	t := annotatedTool{patch: patch}
	if len(t.patch.Effects) > 0 {
		base.Effects = append([]Effect(nil), t.patch.Effects...)
	}
	if t.patch.Exposure != "" {
		base.Exposure = t.patch.Exposure
	}
	if t.patch.Parallel {
		base.Parallel = true
	}
	if t.patch.Exclusive {
		base.Exclusive = true
	}
	if t.patch.Timeout > 0 {
		base.Timeout = t.patch.Timeout
	}
	if len(t.patch.Tags) > 0 {
		base.Tags = append([]string(nil), t.patch.Tags...)
	}
	if len(t.patch.Dependencies) > 0 {
		base.Dependencies = append([]string(nil), t.patch.Dependencies...)
	}
	if len(t.patch.Surfaces) > 0 {
		base.Surfaces = append([]string(nil), t.patch.Surfaces...)
	}
	return base
}

func (t annotatedTool) Execute(ctx context.Context, call Call) (Result, error) {
	return t.inner.Execute(ctx, call)
}

func (t annotatedTool) EndTurn(sessionID, turnID string) {
	if lifecycle, ok := t.inner.(TurnLifecycle); ok {
		lifecycle.EndTurn(sessionID, turnID)
	}
}

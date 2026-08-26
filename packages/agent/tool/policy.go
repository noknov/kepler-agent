package tool

// SurfacePolicy filters tools at catalog construction time.
type SurfacePolicy struct {
	Surface       string
	AvailableDeps map[string]bool
}

func (p SurfacePolicy) Visible(descriptor Descriptor) bool {
	if p.AvailableDeps != nil {
		for _, dep := range descriptor.Dependencies {
			if dep != "" && !p.AvailableDeps[dep] {
				return false
			}
		}
	}
	if p.Surface == "" || len(descriptor.Surfaces) == 0 {
		return true
	}
	for _, surface := range descriptor.Surfaces {
		if surface == p.Surface {
			return true
		}
	}
	return false
}

// RegisterVisible registers item when it passes the surface policy.
func (c *Catalog) RegisterVisible(policy SurfacePolicy, item Tool) error {
	if item == nil {
		return nil
	}
	if !policy.Visible(item.Descriptor()) {
		return nil
	}
	return c.Register(item)
}

// RegisterDeferredVisible registers a deferred tool with a category tag.
func (c *Catalog) RegisterDeferredVisible(policy SurfacePolicy, category string, item Tool) error {
	if item == nil {
		return nil
	}
	patch := Descriptor{Exposure: ExposureDeferred, Tags: []string{category}}
	if category != "" {
		if description, ok := categoryDescriptions[category]; ok && description != "" {
			_ = description
		}
	}
	return c.RegisterVisible(policy, Annotate(item, patch))
}

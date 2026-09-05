package tool

import "fmt"

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
	return c.RegisterVisible(policy, Annotate(item, patch))
}

// Registration collects the first catalog construction error so callers can
// declare a catalog without accidentally discarding descriptor validation or
// duplicate-name failures.
type Registration struct {
	catalog *Catalog
	policy  SurfacePolicy
	err     error
}

func NewRegistration(catalog *Catalog, policy SurfacePolicy) *Registration {
	return &Registration{catalog: catalog, policy: policy}
}

func (r *Registration) Visible(item Tool) {
	if r == nil || r.err != nil {
		return
	}
	if r.catalog == nil {
		r.err = fmt.Errorf("tool catalog is nil")
		return
	}
	r.err = r.catalog.RegisterVisible(r.policy, item)
}

func (r *Registration) Deferred(category string, item Tool) {
	if r == nil || r.err != nil {
		return
	}
	if r.catalog == nil {
		r.err = fmt.Errorf("tool catalog is nil")
		return
	}
	r.err = r.catalog.RegisterDeferredVisible(r.policy, category, item)
}

func (r *Registration) Err() error {
	if r == nil {
		return fmt.Errorf("tool registration is nil")
	}
	return r.err
}

func (r *Registration) Surface() string {
	if r == nil {
		return ""
	}
	return r.policy.Surface
}

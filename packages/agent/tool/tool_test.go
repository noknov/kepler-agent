package tool

import (
	"context"
	"testing"
)

type fakeTool struct{ descriptor Descriptor }

func (f fakeTool) Descriptor() Descriptor                      { return f.descriptor }
func (fakeTool) Execute(context.Context, Call) (Result, error) { return TextResult("ok"), nil }

func TestCatalogOnlyExposesEagerToolsInitially(t *testing.T) {
	catalog, err := NewCatalog(
		fakeTool{descriptor: Descriptor{Name: "eager", Effects: []Effect{EffectRead}, Exposure: ExposureEager}},
		fakeTool{descriptor: Descriptor{Name: "later", Effects: []Effect{EffectRead}, Exposure: ExposureDeferred}},
	)
	if err != nil {
		t.Fatal(err)
	}
	definitions := catalog.ActiveDefinitions("s1")
	if len(definitions) != 1 || definitions[0].Name != "eager" {
		t.Fatalf("definitions = %#v", definitions)
	}
	if err := catalog.Activate("s1", "later"); err != nil {
		t.Fatal(err)
	}
	if got := len(catalog.ActiveDefinitions("s1")); got != 2 {
		t.Fatalf("active definitions = %d", got)
	}
	if got := len(catalog.ActiveDefinitions("s2")); got != 1 {
		t.Fatalf("activation leaked across sessions: %d", got)
	}
	if _, ok := catalog.GetActive("s2", "later"); ok {
		t.Fatal("inactive deferred tool was executable")
	}
}

func TestCatalogRejectsToolWithoutEffects(t *testing.T) {
	if _, err := NewCatalog(fakeTool{descriptor: Descriptor{Name: "implicit"}}); err == nil {
		t.Fatal("expected missing effects to be rejected")
	}
}

func TestBindSurfaceAddsPresentationMetadata(t *testing.T) {
	bound := BindSurface(fakeTool{descriptor: Descriptor{
		Name:    "reminder-create",
		Effects: []Effect{EffectExternalWrite, EffectNetwork},
	}}, "slack", "reminder").Descriptor()
	if len(bound.Surfaces) != 1 || bound.Surfaces[0] != "slack" {
		t.Fatalf("surfaces=%v", bound.Surfaces)
	}
	if len(bound.Dependencies) != 2 || bound.Dependencies[0] != "slack" || bound.Dependencies[1] != "reminder" {
		t.Fatalf("dependencies=%v", bound.Dependencies)
	}
}

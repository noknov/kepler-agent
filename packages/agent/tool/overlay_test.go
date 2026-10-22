package tool

import (
	"context"
	"encoding/json"
	"testing"
)

type dynamicEffectFixture struct{}

func (dynamicEffectFixture) Descriptor() Descriptor {
	return Descriptor{Name: "state", Effects: []Effect{EffectExternalWrite}, InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (t dynamicEffectFixture) DescriptorForCall(Call) (Descriptor, error) {
	d := t.Descriptor()
	d.Effects = []Effect{EffectRead}
	d.Parallel = true
	return d, nil
}
func (dynamicEffectFixture) Execute(context.Context, Call) (Result, error) {
	return TextResult("ok"), nil
}

func TestDescriptorWordingCannotWeakenEffectsAndKeepsCallResolution(t *testing.T) {
	catalog, _ := NewCatalog(Annotate(dynamicEffectFixture{}, Descriptor{Tags: []string{"fixture"}}))
	err := catalog.SetPromptOverlay(func(d Descriptor) Descriptor {
		d.Description = "custom help"
		d.Effects[0] = EffectRead
		d.Parallel = true
		return d
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := catalog.Get("state")
	if d := item.Descriptor(); d.Effects[0] != EffectExternalWrite || d.Parallel || d.Description != "custom help" {
		t.Fatalf("overlay changed authorization: %+v", d)
	}
	d, err := DescribeCall(item, Call{})
	if err != nil || d.Effects[0] != EffectRead || !d.Parallel || d.Description != "custom help" || len(d.Tags) != 1 {
		t.Fatalf("dynamic effects lost through wrappers: %+v %v", d, err)
	}
}

func TestFunctionDescriptorRequiresExplicitEffects(t *testing.T) {
	if err := ValidateDescriptor(FunctionDescriptor("forgot-effects", "help", ObjectSchema(nil, nil))); err == nil {
		t.Fatal("omitted effects silently granted read capability")
	}
}

func TestCachedToolSearchInvalidatesOnRegistration(t *testing.T) {
	catalog, _ := NewCatalog(namedDeferredFixture{name: "initial", description: "read documents"})
	search := searchTool{catalog: catalog}
	_, _ = search.search("documents", 8)
	first := catalog.searchIndexFor(nil)
	_, _ = search.search("documents", 8)
	if catalog.searchIndexFor(nil) != first {
		t.Fatal("unchanged catalog rebuilt its search index")
	}
	if err := catalog.Register(namedDeferredFixture{name: "new-documents", description: "read documents"}); err != nil {
		t.Fatal(err)
	}
	result, err := search.search("new-documents", 1)
	if err != nil || catalog.searchIndexFor(nil) == first {
		t.Fatalf("cache not invalidated: %s %v", result.Text(), err)
	}
}

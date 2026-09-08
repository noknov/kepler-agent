package workflows

import (
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/prompt"
)

type testDefinition struct{}

func (testDefinition) ID() string { return "test" }
func (testDefinition) StartPrompt(string, map[string]string) (Activation, error) {
	return Activation{Prompt: prompt.Fragment{ID: "selected"}, Scope: map[string]string{ScopeWorkflow: "test"}}, nil
}

func (testDefinition) Resume(scope map[string]string) (Activation, bool) {
	if scope[ScopeWorkflow] != "test" {
		return Activation{}, false
	}
	return Activation{Prompt: prompt.Fragment{ID: "resumed"}, OwnsThread: true}, true
}

func TestRegistrySelectsAndResumesDefinition(t *testing.T) {
	registry := NewRegistry(testDefinition{})
	selected, err := registry.StartPrompt("test", "", nil)
	if err != nil || selected.Prompt.ID != "selected" || selected.Scope["workflow"] != "test" {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
	resumed, ok := registry.Resume(selected.Scope)
	if !ok || resumed.Prompt.ID != "resumed" || !resumed.OwnsThread {
		t.Fatalf("resumed=%+v ok=%v", resumed, ok)
	}
}

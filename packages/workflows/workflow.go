// Package workflows defines the transport-neutral lifecycle for product
// workflows selected by a surface and resumed from durable scope.
package workflows

import (
	"fmt"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/prompt"
)

const ScopeWorkflow = "workflow"

// Activation is the complete runtime contract produced by a workflow. Scope
// is persisted on TurnStarted and is therefore the source of truth for resume.
type Activation struct {
	Prompt              prompt.Fragment
	Scope               map[string]string
	RequiredTools       []string
	OwnsThread          bool
	ExposeWorkerResults bool
}

// Definition identifies a workflow and restores it solely from durable scope.
type Definition interface {
	ID() string
	Resume(scope map[string]string) (Activation, bool)
}

// PromptDefinition adapts a semantically selected conversational request into
// a workflow activation. Structured extraction remains owned by the workflow,
// not by the router or the Slack surface.
type PromptDefinition interface {
	Definition
	StartPrompt(text string, inputs map[string]string) (Activation, error)
}

type Registry struct {
	definitions map[string]Definition
}

func NewRegistry(definitions ...Definition) Registry {
	registry := Registry{definitions: make(map[string]Definition, len(definitions))}
	for _, definition := range definitions {
		if definition != nil && definition.ID() != "" {
			registry.definitions[definition.ID()] = definition
		}
	}
	return registry
}

func (r Registry) StartPrompt(id, text string, inputs map[string]string) (Activation, error) {
	definition, ok := r.definitions[id]
	if !ok {
		return Activation{}, fmt.Errorf("unknown workflow %q", id)
	}
	promptDefinition, ok := definition.(PromptDefinition)
	if !ok {
		return Activation{}, fmt.Errorf("workflow %q cannot start from conversation", id)
	}
	return promptDefinition.StartPrompt(text, inputs)
}

func (r Registry) Resume(scope map[string]string) (Activation, bool) {
	id := strings.TrimSpace(scope[ScopeWorkflow])
	definition := r.definitions[id]
	if definition == nil {
		return Activation{}, false
	}
	return definition.Resume(scope)
}

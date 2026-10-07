// Package workflows defines the transport-neutral lifecycle for product
// workflows selected by a surface and resumed from durable scope.
package workflows

import (
	"fmt"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/prompt"
)

const ScopeWorkflow = "workflow"

// OutputPolicy controls how a surface presents model output produced while a
// workflow is running. It is independent of orchestration and transport.
type OutputPolicy string

const (
	OutputStreaming OutputPolicy = "streaming"
	OutputFinalOnly OutputPolicy = "final_only"
)

// Activation is the complete runtime contract produced by a workflow. Scope
// is persisted on TurnStarted and is therefore the source of truth for resume.
type Activation struct {
	Prompt        prompt.Fragment
	Scope         map[string]string
	RequiredTools []string
	OutputPolicy  OutputPolicy
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

// CandidateDefinition optionally gates semantic routing so ordinary messages
// that lack prerequisite structural tokens (such as a GitHub PR URL) skip the
// intent router LLM round-trip entirely.
type CandidateDefinition interface {
	Definition
	CanStartPrompt(text string) bool
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

// HasPromptCandidate reports whether at least one registered conversational
// workflow could plausibly activate for text. If no workflow is registered, it
// returns false so callers can bypass intent classification.
func (r Registry) HasPromptCandidate(text string) bool {
	if len(r.definitions) == 0 {
		return false
	}
	for _, definition := range r.definitions {
		promptDef, ok := definition.(PromptDefinition)
		if !ok {
			continue
		}
		if candidate, ok := promptDef.(CandidateDefinition); ok {
			if candidate.CanStartPrompt(text) {
				return true
			}
			continue
		}
		return true
	}
	return false
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

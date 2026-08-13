package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	agenttool "github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
)

// AdaptRegistry is a construction-only migration boundary for the existing
// tool implementations. Runtime visibility, activation, policy and execution
// are owned exclusively by the canonical agent tool catalog.
func AdaptRegistry(source *registry.Registry) (*agenttool.Catalog, error) {
	if source == nil {
		return nil, fmt.Errorf("hosted tool inventory is nil")
	}
	catalog, err := agenttool.NewCatalog()
	if err != nil {
		return nil, err
	}
	state := &toolRuntimeState{caches: make(map[string]*registry.RuntimeCache)}
	for _, info := range source.Inventory() {
		if info.Spec.Function.Name == "tool_search" {
			continue
		}
		raw, err := json.Marshal(info.Spec.Function.Parameters)
		if err != nil {
			return nil, err
		}
		descriptor := agenttool.Descriptor{
			Name: info.Spec.Function.Name, Description: info.Spec.Function.Description,
			InputSchema: raw, Effects: effectsFor(info.Metadata), Parallel: info.Parallel,
			Dependencies: append([]string(nil), info.Metadata.Dependencies...), Surfaces: append([]string(nil), info.Metadata.Surfaces...),
			Timeout: info.Metadata.Timeout, Exclusive: info.Metadata.Exclusive,
		}
		if info.Deferred {
			descriptor.Exposure = agenttool.ExposureDeferred
			descriptor.Tags = []string{info.Category}
		}
		if err := catalog.Register(registryToolAdapter{state: state, implementation: info.Tool, descriptor: descriptor}); err != nil {
			return nil, err
		}
	}
	if err := catalog.Register(catalogSearch{catalog: catalog}); err != nil {
		return nil, err
	}
	return catalog, nil
}

func effectsFor(metadata registry.ToolMetadata) []agenttool.Effect {
	var effects []agenttool.Effect
	switch metadata.Risk {
	case registry.RiskWrite:
		effects = append(effects, agenttool.EffectWorkspaceWrite)
	case registry.RiskExternalWrite:
		effects = append(effects, agenttool.EffectExternalWrite)
	default:
		effects = append(effects, agenttool.EffectRead)
	}
	if metadata.Network {
		effects = append(effects, agenttool.EffectNetwork)
	}
	return effects
}

type toolRuntimeState struct {
	mu     sync.Mutex
	caches map[string]*registry.RuntimeCache
}

func (s *toolRuntimeState) cache(turnID string) *registry.RuntimeCache {
	s.mu.Lock()
	defer s.mu.Unlock()
	cache := s.caches[turnID]
	if cache == nil {
		cache = registry.NewRuntimeCache()
		s.caches[turnID] = cache
	}
	return cache
}

func (s *toolRuntimeState) endTurn(turnID string) {
	s.mu.Lock()
	delete(s.caches, turnID)
	s.mu.Unlock()
}

type registryToolAdapter struct {
	state          *toolRuntimeState
	implementation registry.Tool
	descriptor     agenttool.Descriptor
}

func (t registryToolAdapter) Descriptor() agenttool.Descriptor { return t.descriptor }
func (t registryToolAdapter) EndTurn(_, turnID string)         { t.state.endTurn(turnID) }
func (t registryToolAdapter) Execute(ctx context.Context, call agenttool.Call) (agenttool.Result, error) {
	values := call.Scope.Values
	result, err := t.implementation.Execute(ctx, call.Arguments, registry.Runtime{
		UserID: call.Scope.UserID, Channel: values["channel"], ThreadTS: values["thread_ts"],
		RunID: call.Scope.TurnID, Cache: t.state.cache(call.Scope.TurnID),
	})
	if err != nil {
		return agenttool.Result{}, err
	}
	return agenttool.Result{Content: []model.Content{{Type: model.ContentText, Text: result.Content}}, NeedsUserInput: result.NeedsUserInput}, nil
}

type catalogSearch struct{ catalog *agenttool.Catalog }

func (catalogSearch) Descriptor() agenttool.Descriptor {
	return agenttool.Descriptor{Name: "tool_search", Description: "List deferred tools and activate them by exact name or category for the current turn.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["list","activate"]},"categories":{"type":"array","items":{"type":"string"}},"tool_names":{"type":"array","items":{"type":"string"}}},"required":["action"]}`), Effects: []agenttool.Effect{agenttool.EffectRead}}
}
func (t catalogSearch) Execute(_ context.Context, call agenttool.Call) (agenttool.Result, error) {
	var args struct {
		Action     string   `json:"action"`
		Categories []string `json:"categories"`
		ToolNames  []string `json:"tool_names"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return agenttool.Result{}, err
	}
	switch args.Action {
	case "list":
		var lines []string
		for _, descriptor := range t.catalog.Descriptors() {
			if descriptor.Exposure == agenttool.ExposureDeferred {
				lines = append(lines, descriptor.Name+" ["+strings.Join(descriptor.Tags, ", ")+"]")
			}
		}
		if len(lines) == 0 {
			return agenttool.TextResult("No deferred tools are available."), nil
		}
		return agenttool.TextResult("Deferred tools (activate exact names or categories):\n- " + strings.Join(lines, "\n- ")), nil
	case "activate":
		names := append([]string(nil), args.ToolNames...)
		wanted := make(map[string]bool, len(args.Categories))
		for _, category := range args.Categories {
			wanted[category] = true
		}
		for _, descriptor := range t.catalog.Descriptors() {
			for _, tag := range descriptor.Tags {
				if wanted[tag] {
					names = append(names, descriptor.Name)
				}
			}
		}
		sort.Strings(names)
		names = compactStrings(names)
		if len(names) == 0 {
			return agenttool.TextResult("No tools requested."), nil
		}
		if err := t.catalog.Activate(call.Scope.SessionID, names...); err != nil {
			return agenttool.Result{}, err
		}
		return agenttool.TextResult("Activated tools:\n- " + strings.Join(names, "\n- ")), nil
	default:
		return agenttool.Result{}, fmt.Errorf("action must be list or activate")
	}
}

func compactStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if value != "" && (len(out) == 0 || out[len(out)-1] != value) {
			out = append(out, value)
		}
	}
	return out
}

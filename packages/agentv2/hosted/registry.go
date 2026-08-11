package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	v2tool "github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

// AdaptRegistry exposes production tools through the canonical catalog. The
// source registry is an immutable template; each v2 session gets an isolated
// clone for deferred activation, and each turn gets one shared runtime cache.
func AdaptRegistry(source *registry.Registry) (*v2tool.Catalog, error) {
	if source == nil {
		return nil, fmt.Errorf("hosted tool registry is nil")
	}
	bridge := &toolBridge{source: source, sessions: make(map[string]*bridgeSession)}
	catalog, err := v2tool.NewCatalog()
	if err != nil {
		return nil, err
	}
	bridge.catalog = catalog
	for _, info := range source.Inventory() {
		raw, err := json.Marshal(info.Spec.Function.Parameters)
		if err != nil {
			return nil, err
		}
		descriptor := v2tool.Descriptor{
			Name: info.Spec.Function.Name, Description: info.Spec.Function.Description,
			InputSchema: raw, Effects: effectsFor(info.Metadata.Risk), Parallel: info.Parallel,
			Dependencies: append([]string(nil), info.Metadata.Dependencies...), Surfaces: append([]string(nil), info.Metadata.Surfaces...), Timeout: info.Metadata.Timeout,
		}
		if info.Deferred {
			descriptor.Exposure = v2tool.ExposureDeferred
			descriptor.Tags = []string{info.Category}
		}
		if err := catalog.Register(bridgedTool{bridge: bridge, descriptor: descriptor}); err != nil {
			return nil, err
		}
	}
	return catalog, nil
}

func effectsFor(risk registry.ToolRisk) []v2tool.Effect {
	switch risk {
	case registry.RiskWrite:
		return []v2tool.Effect{v2tool.EffectWorkspaceWrite}
	case registry.RiskExternalWrite:
		return []v2tool.Effect{v2tool.EffectExternalWrite}
	default:
		return []v2tool.Effect{v2tool.EffectRead}
	}
}

type toolBridge struct {
	mu       sync.Mutex
	source   *registry.Registry
	catalog  *v2tool.Catalog
	sessions map[string]*bridgeSession
}

type bridgeSession struct {
	registry *registry.Registry
	caches   map[string]*registry.RuntimeCache
}

func (b *toolBridge) state(sessionID string) *bridgeSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.sessions[sessionID]
	if state == nil {
		state = &bridgeSession{registry: b.source.Clone(), caches: make(map[string]*registry.RuntimeCache)}
		b.sessions[sessionID] = state
	}
	return state
}

func (b *toolBridge) cache(state *bridgeSession, turnID string) *registry.RuntimeCache {
	b.mu.Lock()
	defer b.mu.Unlock()
	cache := state.caches[turnID]
	if cache == nil {
		cache = registry.NewRuntimeCache()
		state.caches[turnID] = cache
	}
	return cache
}

func (b *toolBridge) syncActive(sessionID string, state *bridgeSession) error {
	for _, spec := range state.registry.Specs() {
		if b.catalog.Has(spec.Function.Name) {
			if err := b.catalog.Activate(sessionID, spec.Function.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

type bridgedTool struct {
	bridge     *toolBridge
	descriptor v2tool.Descriptor
}

func (t bridgedTool) Descriptor() v2tool.Descriptor { return t.descriptor }

func (t bridgedTool) EndTurn(sessionID, turnID string) {
	t.bridge.mu.Lock()
	defer t.bridge.mu.Unlock()
	if state := t.bridge.sessions[sessionID]; state != nil {
		delete(state.caches, turnID)
	}
}

func (t bridgedTool) Execute(ctx context.Context, call v2tool.Call) (v2tool.Result, error) {
	state := t.bridge.state(call.Scope.SessionID)
	values := call.Scope.Values
	result, err := state.registry.Execute(ctx, call.Name, call.Arguments, registry.Runtime{
		UserID: call.Scope.UserID, Channel: values["channel"], ThreadTS: values["thread_ts"],
		RunID: call.Scope.TurnID, Cache: t.bridge.cache(state, call.Scope.TurnID),
	})
	if err != nil {
		return v2tool.Result{}, err
	}
	if err := t.bridge.syncActive(call.Scope.SessionID, state); err != nil {
		return v2tool.Result{}, err
	}
	return v2tool.Result{Content: []model.Content{{Type: model.ContentText, Text: result.Content}}, NeedsUserInput: result.NeedsUserInput}, nil
}

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

// Catalog preserves the existing production tool implementations and their
// capability policy while exposing v2 descriptors and scope.
func AdaptRegistry(source *registry.Registry) (*v2tool.Catalog, error) {
	if source == nil {
		return nil, fmt.Errorf("hosted tool registry is nil")
	}
	bridge := &toolBridge{source: source, known: make(map[string]bool)}
	catalog, err := v2tool.NewCatalog()
	if err != nil {
		return nil, err
	}
	bridge.catalog = catalog
	if err := bridge.sync(""); err != nil {
		return nil, err
	}
	return catalog, nil
}

type toolBridge struct {
	mu      sync.Mutex
	source  *registry.Registry
	catalog *v2tool.Catalog
	known   map[string]bool
}

func (b *toolBridge) sync(sessionID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, spec := range b.source.Specs() {
		name := spec.Function.Name
		if b.known[name] {
			continue
		}
		raw, err := json.Marshal(spec.Function.Parameters)
		if err != nil {
			return err
		}
		descriptor := v2tool.Descriptor{Name: name, Description: spec.Function.Description, InputSchema: raw, Effects: []v2tool.Effect{v2tool.EffectRead}, Parallel: false}
		if sessionID != "" {
			descriptor.Exposure = v2tool.ExposureDeferred
		}
		if decision := b.source.PolicyDecision(name); decision.Risk == registry.RiskWrite {
			descriptor.Effects = []v2tool.Effect{v2tool.EffectWorkspaceWrite}
		} else if decision.Risk == registry.RiskExternalWrite {
			descriptor.Effects = []v2tool.Effect{v2tool.EffectExternalWrite}
		}
		if err := b.catalog.Register(bridgedTool{bridge: b, descriptor: descriptor}); err != nil {
			return err
		}
		b.known[name] = true
		if sessionID != "" {
			_ = b.catalog.Activate(sessionID, name)
		}
	}
	return nil
}

type bridgedTool struct {
	bridge     *toolBridge
	descriptor v2tool.Descriptor
}

func (t bridgedTool) Descriptor() v2tool.Descriptor { return t.descriptor }

func (t bridgedTool) Execute(ctx context.Context, call v2tool.Call) (v2tool.Result, error) {
	values := call.Scope.Values
	result, err := t.bridge.source.Execute(ctx, call.Name, call.Arguments, registry.Runtime{UserID: call.Scope.UserID, Channel: values["channel"], ThreadTS: values["thread_ts"], RunID: call.Scope.TurnID, Cache: registry.NewRuntimeCache()})
	if err != nil {
		return v2tool.Result{}, err
	}
	// tool-search may have activated deferred hosted tools. Make those visible
	// only to the current v2 session on the next model step.
	if err := t.bridge.sync(call.Scope.SessionID); err != nil {
		return v2tool.Result{}, err
	}
	return v2tool.Result{Content: []model.Content{{Type: model.ContentText, Text: result.Content}}, Metadata: map[string]any{"needs_user_input": result.NeedsUserInput}}, nil
}

package hosted

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	v2tool "github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

type bridgeFixtureTool struct {
	name     string
	parallel bool
	pause    bool
}

func (t bridgeFixtureTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(t.name, t.name, registry.ObjectSchema(nil, nil))
}
func (t bridgeFixtureTool) Parallel() bool { return t.parallel }
func (t bridgeFixtureTool) Metadata() registry.ToolMetadata {
	return registry.ToolMetadata{Timeout: 17 * time.Second}
}
func (t bridgeFixtureTool) Execute(_ context.Context, _ json.RawMessage, runtime registry.Runtime) (registry.Result, error) {
	switch t.name {
	case "cache-set":
		runtime.Cache.Set("value", "shared")
		return registry.Result{Content: "stored"}, nil
	case "cache-get":
		value, _ := runtime.Cache.Get("value")
		text, _ := value.(string)
		return registry.Result{Content: text}, nil
	default:
		return registry.Result{Content: "question", NeedsUserInput: t.pause}, nil
	}
}

func TestAdaptRegistryKeepsSessionActivationAndTurnCacheIsolated(t *testing.T) {
	source := registry.NewReadOnly()
	source.Register(bridgeFixtureTool{name: "cache-set", parallel: true})
	source.Register(bridgeFixtureTool{name: "cache-get"})
	source.Register(registry.ToolSearchTool{Registry: source})
	source.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, bridgeFixtureTool{name: "later"}))
	catalog, err := AdaptRegistry(source)
	if err != nil {
		t.Fatal(err)
	}

	set, _ := catalog.GetActive("s1", "cache-set")
	get, _ := catalog.GetActive("s1", "cache-get")
	scope := v2tool.Scope{SessionID: "s1", TurnID: "t1", Values: map[string]string{}}
	if _, err := set.Execute(context.Background(), v2tool.Call{Name: "cache-set", Scope: scope}); err != nil {
		t.Fatal(err)
	}
	result, err := get.Execute(context.Background(), v2tool.Call{Name: "cache-get", Scope: scope})
	if err != nil || len(result.Content) == 0 || result.Content[0].Text != "shared" {
		t.Fatalf("shared turn cache result=%+v err=%v", result, err)
	}
	result, err = get.Execute(context.Background(), v2tool.Call{Name: "cache-get", Scope: v2tool.Scope{SessionID: "s1", TurnID: "t2", Values: map[string]string{}}})
	if err != nil || result.Content[0].Text != "" {
		t.Fatalf("cache leaked across turns: result=%+v err=%v", result, err)
	}
	catalog.EndTurn("s1", "t1")
	if state := set.(bridgedTool).bridge.state("s1"); state.caches["t1"] != nil {
		t.Fatal("turn cache was retained after EndTurn")
	}

	activate := func(session string) {
		t.Helper()
		search, ok := catalog.GetActive(session, "tool_search")
		if !ok {
			t.Fatalf("tool_search inactive for %s", session)
		}
		_, err := search.Execute(context.Background(), v2tool.Call{Name: "tool_search", Arguments: json.RawMessage(`{"action":"activate","tool_names":["later"]}`), Scope: v2tool.Scope{SessionID: session, TurnID: session + "-turn", Values: map[string]string{}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := catalog.GetActive(session, "later"); !ok {
			t.Fatalf("later inactive for %s", session)
		}
	}
	activate("s1")
	if _, ok := catalog.GetActive("s2", "later"); ok {
		t.Fatal("deferred activation leaked across sessions")
	}
	activate("s2")

	descriptors := catalog.Descriptors()
	for _, descriptor := range descriptors {
		if descriptor.Name == "cache-set" {
			if !descriptor.Parallel {
				t.Fatal("parallel metadata was lost")
			}
			if descriptor.Timeout != 17*time.Second {
				t.Fatalf("timeout metadata = %s", descriptor.Timeout)
			}
		}
	}
}

func TestAdaptRegistryPreservesNeedsUserInput(t *testing.T) {
	source := registry.NewReadOnly()
	source.Register(bridgeFixtureTool{name: "ask", pause: true})
	catalog, err := AdaptRegistry(source)
	if err != nil {
		t.Fatal(err)
	}
	item, _ := catalog.GetActive("s", "ask")
	result, err := item.Execute(context.Background(), v2tool.Call{Name: "ask", Scope: v2tool.Scope{SessionID: "s", TurnID: "t", Values: map[string]string{}}})
	if err != nil || !result.NeedsUserInput || !strings.Contains(result.Content[0].Text, "question") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

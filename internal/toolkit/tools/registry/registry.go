package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/prompts"
)

type Runtime struct {
	UserID   string
	Channel  string
	ThreadTS string
	Cache    *RuntimeCache
}

type RuntimeCache struct {
	mu     sync.Mutex
	values map[string]any
}

func NewRuntimeCache() *RuntimeCache {
	return &RuntimeCache{values: map[string]any{}}
}

func (c *RuntimeCache) Get(key string) (any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[key]
	return value, ok
}

func (c *RuntimeCache) Set(key string, value any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = map[string]any{}
	}
	c.values[key] = value
}

type Result struct {
	Content     string
	WaitForUser bool
}

type Tool interface {
	Spec() llm.ToolSpec
	Execute(ctx context.Context, args json.RawMessage, rt Runtime) (Result, error)
}

// RepeatableTool marks a side-effect-free tool whose results may change across
// identical calls (e.g. git fetch, log queries, LLM delegates). The runner
// skips duplicate-call detection for these tools.
type RepeatableTool interface {
	Repeatable() bool
}

type ParallelTool interface {
	Parallel() bool
}

func IsRepeatable(tool Tool) bool {
	if rt, ok := tool.(RepeatableTool); ok {
		return rt.Repeatable()
	}
	return false
}

func CanRunInParallel(tool Tool) bool {
	if pt, ok := tool.(ParallelTool); ok {
		return pt.Parallel()
	}
	return false
}

type Registry struct {
	tools map[string]Tool
}

func New() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(tool Tool) {
	r.tools[tool.Spec().Function.Name] = tool
}

func (r *Registry) Specs() []llm.ToolSpec {
	names := r.Names()
	out := make([]llm.ToolSpec, 0, len(names))
	for _, name := range names {
		out = append(out, r.tools[name].Spec())
	}
	return out
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage, rt Runtime) (Result, error) {
	tool, ok := r.tools[name]
	if !ok {
		return Result{}, fmt.Errorf("unknown tool %q", name)
	}
	return tool.Execute(ctx, args, rt)
}

func (r *Registry) IsRepeatable(name string) bool {
	tool, ok := r.tools[name]
	if !ok {
		return false
	}
	return IsRepeatable(tool)
}

func (r *Registry) CanRunInParallel(name string) bool {
	tool, ok := r.tools[name]
	if !ok {
		return false
	}
	return CanRunInParallel(tool)
}

func FunctionSpec(name, description string, parameters map[string]any) llm.ToolSpec {
	description = prompts.ToolDescription(name, description)
	applyParameterPrompts(name, parameters)
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        name,
			Description: description,
			Parameters:  parameters,
		},
	}
}

func applyParameterPrompts(toolName string, parameters map[string]any) {
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		return
	}
	for param, raw := range properties {
		property, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		current, _ := property["description"].(string)
		if next := prompts.ParamDescription(toolName, param, current); next != "" {
			property["description"] = next
		}
	}
}

func ObjectSchema(required []string, properties map[string]any) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

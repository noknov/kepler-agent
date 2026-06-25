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

// WriteTool marks a tool that performs write/mutate operations (create, update,
// delete, dispatch). When the registry is in read-only mode, these tools are
// registered (so the model sees them in tool specs) but execution is refused.
type WriteTool interface {
	IsWrite() bool
}

// DeferredTool is registered but excluded from default Specs() until its category
// is activated via ActivateCategory or the tool_search tool.
type DeferredTool interface {
	Tool
	Category() string
}

const (
	CategoryDiagnostics = "diagnostics"
	CategoryBrowser       = "browser"
	CategoryIntegration   = "integration"
)

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

func IsWriteOp(tool Tool) bool {
	if wt, ok := tool.(WriteTool); ok {
		return wt.IsWrite()
	}
	return false
}

type Registry struct {
	tools      map[string]Tool
	deferred   map[string]Tool
	categories map[string][]string
	readOnly   bool
}

func New() *Registry {
	return &Registry{
		tools:      map[string]Tool{},
		deferred:   map[string]Tool{},
		categories: map[string][]string{},
	}
}

// NewReadOnly creates a registry that refuses to execute write tools.
// Write tools are still registered (visible to the model) but will return
// a descriptive error when called.
func NewReadOnly() *Registry {
	return &Registry{
		tools:      map[string]Tool{},
		deferred:   map[string]Tool{},
		categories: map[string][]string{},
		readOnly:   true,
	}
}

func (r *Registry) Register(tool Tool) {
	r.tools[tool.Spec().Function.Name] = tool
}

type categorizedTool struct {
	Tool
	category string
}

func (t categorizedTool) Category() string {
	return t.category
}

// AsDeferred wraps a tool with a deferred-tool category label.
func AsDeferred(category string, tool Tool) DeferredTool {
	return categorizedTool{Tool: tool, category: category}
}

func (r *Registry) RegisterDeferred(tool DeferredTool) {
	name := tool.Spec().Function.Name
	r.deferred[name] = tool
	category := tool.Category()
	r.categories[category] = append(r.categories[category], name)
}

// ActivateCategory moves deferred tools in category into the active tool set.
// Returns the names of tools that were activated. Already-active tools are skipped.
func (r *Registry) ActivateCategory(category string) []string {
	names := append([]string(nil), r.categories[category]...)
	activated := make([]string, 0, len(names))
	for _, name := range names {
		tool, ok := r.deferred[name]
		if !ok {
			continue
		}
		r.tools[name] = tool
		delete(r.deferred, name)
		activated = append(activated, name)
	}
	return activated
}

func (r *Registry) DeferredCategories() []string {
	categories := make([]string, 0, len(r.categories))
	for category, names := range r.categories {
		if len(names) == 0 {
			continue
		}
		pending := 0
		for _, name := range names {
			if _, ok := r.deferred[name]; ok {
				pending++
			}
		}
		if pending > 0 {
			categories = append(categories, category)
		}
	}
	sort.Strings(categories)
	return categories
}

func (r *Registry) DeferredToolNames(category string) []string {
	names := append([]string(nil), r.categories[category]...)
	pending := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := r.deferred[name]; ok {
			pending = append(pending, name)
		}
	}
	sort.Strings(pending)
	return pending
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

func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage, rt Runtime) (Result, error) {
	tool, ok := r.tools[name]
	if !ok {
		return Result{}, fmt.Errorf("unknown tool %q", name)
	}
	if r.readOnly && IsWriteOp(tool) {
		return Result{}, fmt.Errorf("tool %q is a write operation and is disabled in read-only mode; use read/query tools instead", name)
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
	staticDescription := prompts.ToolDescription(name, "")
	description = mergeDescription(staticDescription, description)
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

func mergeDescription(staticDescription, dynamicSuffix string) string {
	if staticDescription == "" {
		return dynamicSuffix
	}
	if dynamicSuffix == "" {
		return staticDescription
	}
	return staticDescription + " " + dynamicSuffix
}

func applyParameterPrompts(toolName string, parameters map[string]any) {
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		return
	}
	for param, raw := range properties {
		applyPropertyPrompt(toolName, param, raw)
	}
}

func applyPropertyPrompt(toolName, path string, raw any) {
	property, ok := raw.(map[string]any)
	if !ok {
		return
	}
	current, _ := property["description"].(string)
	if next := prompts.ParamDescription(toolName, path, current); next != "" {
		property["description"] = next
	}
	if nested, ok := property["properties"].(map[string]any); ok {
		for name, child := range nested {
			applyPropertyPrompt(toolName, path+"."+name, child)
		}
	}
	items, ok := property["items"].(map[string]any)
	if !ok {
		return
	}
	current, _ = items["description"].(string)
	if next := prompts.ParamDescription(toolName, path+".items", current); next != "" {
		items["description"] = next
	}
	if nested, ok := items["properties"].(map[string]any); ok {
		for name, child := range nested {
			applyPropertyPrompt(toolName, path+".items."+name, child)
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

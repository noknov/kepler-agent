package registry

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
)

type Runtime struct {
	UserID   string
	Channel  string
	ThreadTS string
	RunID    string
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
	Content string

	// NeedsUserInput means the tool is intentionally pausing the run because
	// the model lacks a preference, direction, or missing detail from the user.
	// This is not a permission approval mechanism.
	NeedsUserInput bool
}

type Tool interface {
	Spec() llm.ToolSpec
	Execute(ctx context.Context, args json.RawMessage, rt Runtime) (Result, error)
}

type ParallelTool interface {
	Parallel() bool
}

// WriteTool marks a tool that performs write/mutate operations (create, update,
// delete, dispatch). Capability policy decides whether these tools are exposed.
type WriteTool interface {
	IsWrite() bool
}

type ToolRisk string

const (
	RiskRead          ToolRisk = "read"
	RiskWrite         ToolRisk = "write"
	RiskExternalWrite ToolRisk = "external_write"
)

type ToolMetadata struct {
	Risk         ToolRisk
	Dependencies []string
	Surfaces     []string
	Timeout      time.Duration
	Exclusive    bool
	Network      bool
}

// ToolInfo is an immutable catalog view used by product adapters. Deferred
// tools remain discoverable without mutating the registry that owns them.
type ToolInfo struct {
	Tool     Tool
	Spec     llm.ToolSpec
	Metadata ToolMetadata
	Parallel bool
	Deferred bool
	Category string
}

type MetadataTool interface {
	Metadata() ToolMetadata
}

// DeferredTool is registered as a construction-time inventory item. The agent
// catalog owns all session-scoped activation.
type DeferredTool interface {
	Tool
	Category() string
}

type CapabilityPolicy struct {
	Surface       string
	AvailableDeps map[string]bool
}

const (
	CategoryDiagnostics    = "diagnostics"
	CategoryCode           = "code"
	CategoryIntegration    = "integration"
	CategoryInfrastructure = "infrastructure"
)

func CanRunInParallel(tool Tool) bool {
	if pt, ok := tool.(ParallelTool); ok {
		return pt.Parallel()
	}
	return false
}

func IsWriteOp(tool Tool) bool {
	meta := MetadataOf(tool)
	if meta.Risk == RiskWrite || meta.Risk == RiskExternalWrite {
		return true
	}
	if wt, ok := tool.(WriteTool); ok {
		return wt.IsWrite()
	}
	return false
}

func MetadataOf(tool Tool) ToolMetadata {
	if mt, ok := tool.(MetadataTool); ok {
		return mt.Metadata()
	}
	if wt, ok := tool.(WriteTool); ok && wt.IsWrite() {
		return ToolMetadata{Risk: RiskWrite}
	}
	return ToolMetadata{Risk: RiskRead}
}

type Registry struct {
	mu       sync.RWMutex
	tools    map[string]Tool
	deferred map[string]Tool
	policy   CapabilityPolicy
}

func NewWithPolicy(policy CapabilityPolicy) *Registry {
	return &Registry{
		tools:    map[string]Tool{},
		deferred: map[string]Tool{},
		policy:   policy,
	}
}

// Inventory returns every policy-visible eager and deferred tool. Unlike
// the former runtime registry, it cannot execute or activate tools.
func (r *Registry) Inventory() []ToolInfo {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]ToolInfo, 0, len(r.tools)+len(r.deferred))
	for _, item := range r.tools {
		if r.canExpose(item) {
			items = append(items, ToolInfo{Tool: item, Spec: item.Spec(), Metadata: MetadataOf(item), Parallel: !IsWriteOp(item) && CanRunInParallel(item)})
		}
	}
	for _, item := range r.deferred {
		if !r.canExpose(item) {
			continue
		}
		category := ""
		if deferred, ok := item.(DeferredTool); ok {
			category = deferred.Category()
		}
		items = append(items, ToolInfo{Tool: item, Spec: item.Spec(), Metadata: MetadataOf(item), Parallel: !IsWriteOp(item) && CanRunInParallel(item), Deferred: true, Category: category})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Spec.Function.Name < items[j].Spec.Function.Name })
	return items
}

func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Spec().Function.Name] = tool
}

type categorizedTool struct {
	Tool
	category string
}

func (t categorizedTool) Category() string {
	return t.category
}

func (t categorizedTool) Parallel() bool {
	return CanRunInParallel(t.Tool)
}

func (t categorizedTool) IsWrite() bool {
	return IsWriteOp(t.Tool)
}

func (t categorizedTool) Metadata() ToolMetadata {
	return MetadataOf(t.Tool)
}

// AsDeferred wraps a tool with a deferred-tool category label.
func AsDeferred(category string, tool Tool) DeferredTool {
	return categorizedTool{Tool: tool, category: category}
}

type metadataTool struct {
	Tool
	metadata ToolMetadata
}

func WithMetadata(tool Tool, metadata ToolMetadata) Tool {
	return metadataTool{Tool: tool, metadata: metadata}
}

func (t metadataTool) Metadata() ToolMetadata {
	base := MetadataOf(t.Tool)
	if t.metadata.Risk != "" {
		base.Risk = t.metadata.Risk
	}
	if len(t.metadata.Dependencies) > 0 {
		base.Dependencies = append([]string(nil), t.metadata.Dependencies...)
	}
	if len(t.metadata.Surfaces) > 0 {
		base.Surfaces = append([]string(nil), t.metadata.Surfaces...)
	}
	base.Exclusive = base.Exclusive || t.metadata.Exclusive
	base.Network = base.Network || t.metadata.Network
	return base
}

func (t metadataTool) IsWrite() bool {
	return IsWriteOp(t.Tool)
}

func (t metadataTool) Parallel() bool {
	return CanRunInParallel(t.Tool)
}

func (r *Registry) RegisterDeferred(tool DeferredTool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Spec().Function.Name
	r.deferred[name] = tool
}

func (r *Registry) canExpose(tool Tool) bool {
	return r.policyDecisionLocked(tool).Allowed
}

func (r *Registry) policyDecisionLocked(tool Tool) policyDecision {
	meta := MetadataOf(tool)
	decision := policyDecision{}
	if r.policy.AvailableDeps != nil {
		for _, dep := range meta.Dependencies {
			if dep != "" && !r.policy.AvailableDeps[dep] {
				decision.Reason = "missing dependency: " + dep
				return decision
			}
		}
	}
	surfaceMatched := r.policy.Surface != "" && len(meta.Surfaces) > 0 && stringInSet(r.policy.Surface, meta.Surfaces)
	surfaceAllowed := r.policy.Surface == "" || len(meta.Surfaces) == 0 || surfaceMatched
	if !surfaceAllowed {
		decision.Reason = "tool is unavailable on surface: " + r.policy.Surface
		return decision
	}
	decision.Allowed = true
	decision.Reason = "available on this surface"
	return decision
}

type policyDecision struct {
	Allowed bool
	Reason  string
}

func stringInSet(value string, set []string) bool {
	for _, item := range set {
		if item == value {
			return true
		}
	}
	return false
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

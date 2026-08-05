package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
)

type Runtime struct {
	UserID         string
	Channel        string
	ThreadTS       string
	RunID          string
	Cache          *RuntimeCache
	ToolSpillStore ToolSpillStore
}

type ToolSpillStore interface {
	SaveToolSpill(ctx context.Context, runID, toolName, toolCallID, content string) error
	ReadToolSpill(ctx context.Context, runID, toolName, toolCallID string) (string, error)
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
}

type MetadataTool interface {
	Metadata() ToolMetadata
}

// DeferredTool is registered but excluded from default Specs() until its category
// is activated via ActivateCategory or the tool_search tool.
type DeferredTool interface {
	Tool
	Category() string
}

// CloneableTool lets tools that hold a registry pointer re-bind themselves when
// a per-run registry clone is created.
type CloneableTool interface {
	CloneForRegistry(*Registry) Tool
}

type CapabilityPolicy struct {
	Surface           string
	AllowWrites       bool
	AllowedWriteTools map[string]bool
	AllowedRisks      map[ToolRisk]bool
	AvailableDeps     map[string]bool
}

const (
	CategoryDiagnostics    = "diagnostics"
	CategoryBrowser        = "browser"
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
	mu         sync.RWMutex
	tools      map[string]Tool
	deferred   map[string]Tool
	categories map[string][]string
	policy     CapabilityPolicy
	specsCache []llm.ToolSpec
	namesCache []string
	cacheValid bool
}

func New() *Registry {
	return NewWithPolicy(CapabilityPolicy{AllowWrites: true})
}

func NewWithPolicy(policy CapabilityPolicy) *Registry {
	return &Registry{
		tools:      map[string]Tool{},
		deferred:   map[string]Tool{},
		categories: map[string][]string{},
		policy:     policy,
	}
}

// NewReadOnly creates a registry whose exposed tool surface excludes write tools.
// Execute still refuses write tools as a defense-in-depth fallback for stale contexts.
func NewReadOnly() *Registry {
	return NewWithPolicy(CapabilityPolicy{AllowWrites: false})
}

// NewReadOnlyWithAllowedWrites creates a registry that hides write tools except
// for explicit, product-approved exceptions.
func NewReadOnlyWithAllowedWrites(names ...string) *Registry {
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		if name != "" {
			allowed[name] = true
		}
	}
	return NewWithPolicy(CapabilityPolicy{AllowedWriteTools: allowed})
}

// Clone creates an isolated registry for a single agent run. Tool instances are
// shared unless they opt into CloneableTool, so expensive clients remain reused
// while dynamic activation state stays local to the run.
func (r *Registry) Clone() *Registry {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	clone := &Registry{
		tools:      make(map[string]Tool, len(r.tools)),
		deferred:   make(map[string]Tool, len(r.deferred)),
		categories: make(map[string][]string, len(r.categories)),
		policy: CapabilityPolicy{
			AllowWrites:       r.policy.AllowWrites,
			Surface:           r.policy.Surface,
			AllowedWriteTools: copyBoolMap(r.policy.AllowedWriteTools),
			AllowedRisks:      copyRiskMap(r.policy.AllowedRisks),
			AvailableDeps:     copyBoolMap(r.policy.AvailableDeps),
		},
	}
	for name, tool := range r.tools {
		clone.tools[name] = cloneToolForRegistry(tool, clone)
	}
	for name, tool := range r.deferred {
		clone.deferred[name] = cloneToolForRegistry(tool, clone)
	}
	for category, names := range r.categories {
		clone.categories[category] = append([]string(nil), names...)
	}
	return clone
}

func cloneToolForRegistry(tool Tool, reg *Registry) Tool {
	if ct, ok := tool.(CloneableTool); ok {
		return ct.CloneForRegistry(reg)
	}
	return tool
}

func copyBoolMap(in map[string]bool) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyRiskMap(in map[ToolRisk]bool) map[ToolRisk]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[ToolRisk]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Spec().Function.Name] = tool
	r.cacheValid = false
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

func (t categorizedTool) CloneForRegistry(reg *Registry) Tool {
	t.Tool = cloneToolForRegistry(t.Tool, reg)
	return t
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
	return base
}

func (t metadataTool) IsWrite() bool {
	return IsWriteOp(t.Tool)
}

func (t metadataTool) Parallel() bool {
	return CanRunInParallel(t.Tool)
}

func (t metadataTool) CloneForRegistry(reg *Registry) Tool {
	t.Tool = cloneToolForRegistry(t.Tool, reg)
	return t
}

func (r *Registry) RegisterDeferred(tool DeferredTool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Spec().Function.Name
	r.deferred[name] = tool
	category := tool.Category()
	r.categories[category] = append(r.categories[category], name)
	r.cacheValid = false
}

// ActivateCategory moves deferred tools in category into the active tool set.
// Returns the names of tools that were activated. Already-active tools are skipped.
func (r *Registry) ActivateCategory(category string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activateCategoryLocked(category)
}

func (r *Registry) activateCategoryLocked(category string) []string {
	names := append([]string(nil), r.categories[category]...)
	activated := make([]string, 0, len(names))
	for _, name := range names {
		if r.activateToolLocked(name) {
			activated = append(activated, name)
		}
	}
	return activated
}

// ActivateTool moves one deferred tool into the active tool set.
func (r *Registry) ActivateTool(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activateToolLocked(name)
}

func (r *Registry) activateToolLocked(name string) bool {
	tool, ok := r.deferred[name]
	if !ok {
		return false
	}
	if !r.canExpose(name, tool) {
		return false
	}
	r.tools[name] = tool
	delete(r.deferred, name)
	r.cacheValid = false
	return true
}

func (r *Registry) DeferredCategories() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	categories := make([]string, 0, len(r.categories))
	for category, names := range r.categories {
		if len(names) == 0 {
			continue
		}
		pending := 0
		for _, name := range names {
			if tool, ok := r.deferred[name]; ok && r.canExpose(name, tool) {
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
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := append([]string(nil), r.categories[category]...)
	pending := make([]string, 0, len(names))
	for _, name := range names {
		if tool, ok := r.deferred[name]; ok && r.canExpose(name, tool) {
			pending = append(pending, name)
		}
	}
	sort.Strings(pending)
	return pending
}

func (r *Registry) Specs() []llm.ToolSpec {
	r.mu.RLock()
	if r.cacheValid && r.specsCache != nil {
		out := make([]llm.ToolSpec, len(r.specsCache))
		copy(out, r.specsCache)
		r.mu.RUnlock()
		return out
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cacheValid && r.specsCache != nil {
		out := make([]llm.ToolSpec, len(r.specsCache))
		copy(out, r.specsCache)
		return out
	}
	names := r.namesLocked()
	specs := make([]llm.ToolSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, r.tools[name].Spec())
	}
	r.specsCache = specs
	r.namesCache = names
	r.cacheValid = true
	out := make([]llm.ToolSpec, len(specs))
	copy(out, specs)
	return out
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	if r.cacheValid && r.namesCache != nil {
		out := make([]string, len(r.namesCache))
		copy(out, r.namesCache)
		r.mu.RUnlock()
		return out
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	names := r.namesLocked()
	r.namesCache = names
	out := make([]string, len(names))
	copy(out, names)
	return out
}

func (r *Registry) namesLocked() []string {
	names := make([]string, 0, len(r.tools))
	for name, tool := range r.tools {
		if !r.canExpose(name, tool) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return ok && r.canExpose(name, tool)
}

func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage, rt Runtime) (Result, error) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	if !ok {
		r.mu.RUnlock()
		return Result{}, fmt.Errorf("unknown tool %q", name)
	}
	if !r.canExpose(name, tool) {
		r.mu.RUnlock()
		return Result{}, fmt.Errorf("tool %q is a write operation and is disabled by server capability policy", name)
	}
	r.mu.RUnlock()
	return tool.Execute(ctx, args, rt)
}

func (r *Registry) canExpose(name string, tool Tool) bool {
	meta := MetadataOf(tool)
	if r.policy.AvailableDeps != nil {
		for _, dep := range meta.Dependencies {
			if dep != "" && !r.policy.AvailableDeps[dep] {
				return false
			}
		}
	}
	surfaceMatched := r.policy.Surface != "" && len(meta.Surfaces) > 0 && stringInSet(r.policy.Surface, meta.Surfaces)
	surfaceAllowed := r.policy.Surface == "" || len(meta.Surfaces) == 0 || surfaceMatched
	if !surfaceAllowed {
		return false
	}
	if meta.Risk == "" || meta.Risk == RiskRead {
		return true
	}
	if r.policy.AllowedRisks[meta.Risk] {
		return true
	}
	return r.policy.AllowWrites || r.policy.AllowedWriteTools[name]
}

func stringInSet(value string, set []string) bool {
	for _, item := range set {
		if item == value {
			return true
		}
	}
	return false
}

func (r *Registry) CanRunInParallel(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	if !ok {
		return false
	}
	return !IsWriteOp(tool) && CanRunInParallel(tool)
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

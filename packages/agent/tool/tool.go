// Package tool defines transport-neutral tools, policy, and catalogs.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

type Effect string

const (
	EffectRead           Effect = "read"
	EffectWorkspaceWrite Effect = "workspace_write"
	EffectExternalWrite  Effect = "external_write"
	EffectPrivileged     Effect = "privileged"
	EffectNetwork        Effect = "network"
)

type Exposure string

const (
	ExposureEager    Exposure = "eager"
	ExposureDeferred Exposure = "deferred"
	ExposureDisabled Exposure = "disabled"
)

type Descriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Effects     []Effect        `json:"effects,omitempty"`
	Exposure    Exposure        `json:"exposure,omitempty"`
	Parallel    bool            `json:"parallel,omitempty"`
	// Exclusive tools, such as a request for missing user input, must be the
	// only call in a model-produced tool batch. This prevents a write from being
	// executed before the run pauses for an answer.
	Exclusive    bool          `json:"exclusive,omitempty"`
	Timeout      time.Duration `json:"timeout,omitempty"`
	Tags         []string      `json:"tags,omitempty"`
	Dependencies []string      `json:"dependencies,omitempty"`
	Surfaces     []string      `json:"surfaces,omitempty"`
}

func (d Descriptor) Definition() model.ToolDefinition {
	return model.ToolDefinition{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema}
}

type Scope struct {
	SessionID string            `json:"session_id"`
	TurnID    string            `json:"turn_id"`
	UserID    string            `json:"user_id,omitempty"`
	Workspace string            `json:"workspace,omitempty"`
	Values    map[string]string `json:"values,omitempty"`
}

type Call struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Scope     Scope           `json:"scope"`
}

type Result struct {
	Content        []model.Content `json:"content,omitempty"`
	IsError        bool            `json:"is_error,omitempty"`
	ErrorCode      string          `json:"error_code,omitempty"`
	Retryable      bool            `json:"retryable,omitempty"`
	Metadata       map[string]any  `json:"metadata,omitempty"`
	Truncated      bool            `json:"truncated,omitempty"`
	Spill          *model.Artifact `json:"spill,omitempty"`
	NeedsUserInput bool            `json:"needs_user_input,omitempty"`
}

func TextResult(value string) Result {
	return Result{Content: []model.Content{{Type: model.ContentText, Text: value}}}
}

// Text returns concatenated text content blocks from a tool result.
func (r Result) Text() string {
	var parts []string
	for _, item := range r.Content {
		if item.Type == model.ContentText && item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, "")
}

type Tool interface {
	Descriptor() Descriptor
	Execute(ctx context.Context, call Call) (Result, error)
}

// TurnLifecycle lets an adapter release turn-scoped state after every exit
// path. Implementations must be idempotent because a catalog may contain
// multiple tools backed by the same adapter.
type TurnLifecycle interface {
	EndTurn(sessionID, turnID string)
}

type DecisionType string

const (
	DecisionAllow           DecisionType = "allow"
	DecisionDeny            DecisionType = "deny"
	DecisionRequireApproval DecisionType = "require_approval"
)

type Decision struct {
	Type        DecisionType   `json:"type"`
	Reason      string         `json:"reason,omitempty"`
	Rule        string         `json:"rule,omitempty"`
	Constraints map[string]any `json:"constraints,omitempty"`
}

type PolicyRequest struct {
	Descriptor Descriptor `json:"descriptor"`
	Call       Call       `json:"call"`
}

type Policy interface {
	Decide(ctx context.Context, request PolicyRequest) (Decision, error)
}

type Approver interface {
	Approve(ctx context.Context, request PolicyRequest, decision Decision) (bool, error)
}

type AllowAllPolicy struct{}

func (AllowAllPolicy) Decide(context.Context, PolicyRequest) (Decision, error) {
	return Decision{Type: DecisionAllow}, nil
}

// ReadOnlyPolicy is the safe default for embeddings that do not provide a
// product policy. Mutating, privileged and network effects require an explicit
// policy decision from the composing profile.
type ReadOnlyPolicy struct{}

func (ReadOnlyPolicy) Decide(_ context.Context, request PolicyRequest) (Decision, error) {
	for _, effect := range request.Descriptor.Effects {
		if effect != EffectRead {
			return Decision{Type: DecisionDeny, Reason: "non-read effect requires an explicit product policy", Rule: "default-read-only"}, nil
		}
	}
	return Decision{Type: DecisionAllow, Rule: "default-read-only"}, nil
}

type Catalog struct {
	mu     sync.RWMutex
	tools  map[string]Tool
	eager  map[string]bool
	active map[string]map[string]bool
}

func NewCatalog(items ...Tool) (*Catalog, error) {
	catalog := &Catalog{tools: make(map[string]Tool), eager: make(map[string]bool), active: make(map[string]map[string]bool)}
	for _, item := range items {
		if err := catalog.Register(item); err != nil {
			return nil, err
		}
	}
	return catalog, nil
}

func (c *Catalog) Register(item Tool) error {
	if item == nil {
		return fmt.Errorf("tool is nil")
	}
	descriptor := item.Descriptor()
	if descriptor.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if len(descriptor.Effects) == 0 {
		return fmt.Errorf("tool %q must declare at least one effect", descriptor.Name)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.tools[descriptor.Name]; exists {
		return fmt.Errorf("tool %q is already registered", descriptor.Name)
	}
	c.tools[descriptor.Name] = item
	if descriptor.Exposure == "" || descriptor.Exposure == ExposureEager {
		c.eager[descriptor.Name] = true
	}
	return nil
}

func (c *Catalog) Get(name string) (Tool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	item, ok := c.tools[name]
	return item, ok
}

func (c *Catalog) Has(name string) bool {
	_, ok := c.Get(name)
	return ok
}

func (c *Catalog) GetActive(sessionID, name string) (Tool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	item, ok := c.tools[name]
	if !ok || item.Descriptor().Exposure == ExposureDisabled {
		return nil, false
	}
	if c.eager[name] || c.active[sessionID][name] {
		return item, true
	}
	return nil, false
}

func (c *Catalog) Activate(sessionID string, names ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("session id is required to activate tools")
	}
	if c.active[sessionID] == nil {
		c.active[sessionID] = make(map[string]bool)
	}
	for _, name := range names {
		item, ok := c.tools[name]
		if !ok {
			return fmt.Errorf("unknown tool %q", name)
		}
		if item.Descriptor().Exposure == ExposureDisabled {
			return fmt.Errorf("tool %q is disabled", name)
		}
		c.active[sessionID][name] = true
	}
	return nil
}

func (c *Catalog) ActiveDefinitions(sessionID string) []model.ToolDefinition {
	c.mu.RLock()
	defer c.mu.RUnlock()
	definitions := make([]model.ToolDefinition, 0, len(c.eager)+len(c.active[sessionID]))
	for name, item := range c.tools {
		if c.eager[name] || c.active[sessionID][name] {
			definitions = append(definitions, item.Descriptor().Definition())
		}
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	return definitions
}

func (c *Catalog) Descriptors() []Descriptor {
	c.mu.RLock()
	defer c.mu.RUnlock()
	descriptors := make([]Descriptor, 0, len(c.tools))
	for _, item := range c.tools {
		descriptors = append(descriptors, item.Descriptor())
	}
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].Name < descriptors[j].Name })
	return descriptors
}

func (c *Catalog) EndTurn(sessionID, turnID string) {
	c.mu.RLock()
	items := make([]Tool, 0, len(c.tools))
	for _, item := range c.tools {
		items = append(items, item)
	}
	c.mu.RUnlock()
	for _, item := range items {
		if lifecycle, ok := item.(TurnLifecycle); ok {
			lifecycle.EndTurn(sessionID, turnID)
		}
	}
	ClearTurnCache(sessionID, turnID)
	c.Deactivate(sessionID)
}

// Deactivate releases turn-scoped deferred-tool visibility.
func (c *Catalog) Deactivate(sessionID string) {
	c.mu.Lock()
	delete(c.active, sessionID)
	c.mu.Unlock()
}

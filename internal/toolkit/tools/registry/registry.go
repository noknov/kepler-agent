package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/prompts"
)

type Runtime struct {
	UserID            string
	Channel           string
	ThreadTS          string
	ConfirmedActions  map[string]bool
	ConfirmTools      map[string]bool
	SensitivePatterns []string
}

type Result struct {
	Content          string
	WaitForUser      bool
	PendingActionKey string
}

type Tool interface {
	Spec() llm.ToolSpec
	Execute(ctx context.Context, args json.RawMessage, rt Runtime) (Result, error)
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

func ActionKey(name string, payload any) string {
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte(fmt.Sprintf("%v", payload))
	}
	sum := sha256.Sum256(append([]byte(name+"\x00"), data...))
	return "action:" + hex.EncodeToString(sum[:12])
}

func ConfirmationState(rt Runtime, name string, payload any) (string, bool) {
	key := ActionKey(name, payload)
	return key, rt.ConfirmedActions != nil && rt.ConfirmedActions[key]
}

func ToolNeedsConfirmation(rt Runtime, name string) bool {
	if rt.ConfirmTools == nil {
		return false
	}
	return rt.ConfirmTools[name]
}

func IsSensitiveText(text string, patterns []string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return false
	}
	if len(patterns) == 0 {
		patterns = DefaultSensitivePatterns()
	}
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern != "" && strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func LooksProductionScoped(values ...string) bool {
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		for _, marker := range []string{"prod", "production", "live"} {
			if strings.Contains(value, marker) {
				return true
			}
		}
	}
	return false
}

func DefaultSensitivePatterns() []string {
	return []string{".env", "secret", "token", "credential", "credentials", "private_key", "id_rsa", ".pem", ".key", ".kube", "kubeconfig", "service-account"}
}

package codeintel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	engine "github.com/wati/oncall-agent/internal/codeintel"
	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type SymbolsTool struct {
	Manager engine.Manager
}

func (SymbolsTool) Parallel() bool { return true }

func (t SymbolsTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"code-symbols",
		"",
		registry.ObjectSchema([]string{"query"}, map[string]any{
			"repo":  map[string]any{"type": "string", "description": ""},
			"query": map[string]any{"type": "string", "description": ""},
			"limit": map[string]any{"type": "integer", "description": ""},
		}),
	)
}

func (t SymbolsTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo  string `json:"repo"`
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	symbols, err := t.Manager.Symbols(ctx, args.Repo, args.Query, args.Limit)
	if err != nil {
		return registry.Result{}, err
	}
	if len(symbols) == 0 {
		return registry.Result{Content: "no symbols found"}, nil
	}
	var b strings.Builder
	b.WriteString("Symbols\n")
	for _, sym := range symbols {
		container := ""
		if sym.Container != "" {
			container = " container=" + sym.Container
		}
		b.WriteString(fmt.Sprintf("- %s %s %s:%d:%d%s\n", sym.KindName, sym.Name, sym.Path, sym.Line, sym.Character, container))
	}
	return registry.Result{Content: strings.TrimSpace(b.String())}, nil
}

type DefinitionTool struct {
	Manager engine.Manager
}

func (DefinitionTool) Parallel() bool { return true }

func (t DefinitionTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"code-definition",
		"",
		positionSchema(),
	)
}

func (t DefinitionTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	pos, err := decodePosition(raw)
	if err != nil {
		return registry.Result{}, err
	}
	locations, err := t.Manager.Definition(ctx, pos)
	if err != nil {
		return registry.Result{}, err
	}
	return formatLocations("Definitions", locations), nil
}

type ReferencesTool struct {
	Manager engine.Manager
}

func (ReferencesTool) Parallel() bool { return true }

func (t ReferencesTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"code-references",
		"",
		positionSchema(),
	)
}

func (t ReferencesTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	pos, err := decodePosition(raw)
	if err != nil {
		return registry.Result{}, err
	}
	locations, err := t.Manager.References(ctx, pos)
	if err != nil {
		return registry.Result{}, err
	}
	return formatLocations("References", locations), nil
}

type DiagnosticsTool struct {
	Manager engine.Manager
}

func (DiagnosticsTool) Parallel() bool { return true }

func (t DiagnosticsTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"code-diagnostics",
		"",
		registry.ObjectSchema([]string{"path"}, map[string]any{
			"repo": map[string]any{"type": "string", "description": ""},
			"path": map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (t DiagnosticsTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	diagnostics, err := t.Manager.Diagnostics(ctx, args.Repo, args.Path)
	if err != nil {
		return registry.Result{}, err
	}
	if len(diagnostics) == 0 {
		return registry.Result{Content: "no diagnostics"}, nil
	}
	var b strings.Builder
	b.WriteString("Diagnostics\n")
	for _, d := range diagnostics {
		source := ""
		if d.Source != "" {
			source = " source=" + d.Source
		}
		b.WriteString(fmt.Sprintf("- severity=%d %s:%d:%d%s %s\n", d.Severity, d.Path, d.Line, d.Character, source, d.Message))
	}
	return registry.Result{Content: strings.TrimSpace(b.String())}, nil
}

func positionSchema() map[string]any {
	return registry.ObjectSchema([]string{"path", "line", "character"}, map[string]any{
		"repo":      map[string]any{"type": "string", "description": ""},
		"path":      map[string]any{"type": "string", "description": ""},
		"line":      map[string]any{"type": "integer", "description": ""},
		"character": map[string]any{"type": "integer", "description": ""},
	})
}

func decodePosition(raw json.RawMessage) (engine.Position, error) {
	var args struct {
		Repo      string `json:"repo"`
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Character int    `json:"character"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return engine.Position{}, err
	}
	return engine.Position{Repo: args.Repo, Path: args.Path, Line: args.Line, Character: args.Character}, nil
}

func formatLocations(title string, locations []engine.Location) registry.Result {
	if len(locations) == 0 {
		return registry.Result{Content: strings.ToLower(title) + " not found"}
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	for _, loc := range locations {
		b.WriteString(fmt.Sprintf("- %s:%d:%d\n", loc.Path, loc.Line, loc.Character))
	}
	return registry.Result{Content: strings.TrimSpace(b.String())}
}

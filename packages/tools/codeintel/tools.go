package codeintel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	engine "github.com/noknov/slack-copilot-agent/packages/codeintel"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type SymbolsTool struct {
	Manager engine.Manager
}


func (t SymbolsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"code-symbols",
		"Search workspace symbols with LSP. Use this to locate candidate functions, classes, methods, or variables before reading their files. Symbol hits are hints; follow up with code-read_file or code-definition before claiming behavior.",
		tool.ObjectSchema([]string{"query"}, map[string]any{
			"repo":  map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name."},
			"query": map[string]any{"type": "string", "description": "Symbol name or prefix to search for."},
			"limit": map[string]any{"type": "integer", "description": "Maximum symbols, default 20 and max 100."},
		}),
	)
}

func (t SymbolsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Repo  string `json:"repo"`
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	symbols, err := t.Manager.Symbols(ctx, args.Repo, args.Query, args.Limit)
	if err != nil {
		return tool.Result{}, err
	}
	if len(symbols) == 0 {
		return tool.TextResult("no symbols found"), nil
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
	return tool.TextResult(strings.TrimSpace(b.String())), nil
}

type DefinitionTool struct {
	Manager engine.Manager
}


func (t DefinitionTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"code-definition",
		"Find the definition of the symbol at a 1-based file position. Use after reading or locating a call site; then read the returned file/range before making behavior claims.",
		positionSchema(),
	)
}

func (t DefinitionTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	pos, err := decodePosition(call.Arguments)
	if err != nil {
		return tool.Result{}, err
	}
	locations, err := t.Manager.Definition(ctx, pos)
	if err != nil {
		return tool.Result{}, err
	}
	return formatLocations("Definitions", locations), nil
}

type ReferencesTool struct {
	Manager engine.Manager
}


func (t ReferencesTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"code-references",
		"Find references to the symbol at a 1-based file position. Use this to verify call sites and wiring instead of inferring from names alone.",
		positionSchema(),
	)
}

func (t ReferencesTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	pos, err := decodePosition(call.Arguments)
	if err != nil {
		return tool.Result{}, err
	}
	locations, err := t.Manager.References(ctx, pos)
	if err != nil {
		return tool.Result{}, err
	}
	return formatLocations("References", locations), nil
}

type ImplementationTool struct {
	Manager engine.Manager
}


func (t ImplementationTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"code-implementation",
		"Find implementations of the interface, abstract method, or symbol at a 1-based file position. Use this when definition points to an interface or contract and behavior lives in concrete implementations.",
		positionSchema(),
	)
}

func (t ImplementationTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	pos, err := decodePosition(call.Arguments)
	if err != nil {
		return tool.Result{}, err
	}
	locations, err := t.Manager.Implementation(ctx, pos)
	if err != nil {
		return tool.Result{}, err
	}
	return formatLocations("Implementations", locations), nil
}

type IncomingCallsTool struct {
	Manager engine.Manager
}


func (t IncomingCallsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"code-incoming_calls",
		"Find functions/methods that call the symbol at a 1-based file position. Use this to verify entry points and callers instead of inferring wiring from names.",
		positionSchema(),
	)
}

func (t IncomingCallsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	pos, err := decodePosition(call.Arguments)
	if err != nil {
		return tool.Result{}, err
	}
	calls, err := t.Manager.IncomingCalls(ctx, pos)
	if err != nil {
		return tool.Result{}, err
	}
	return formatCalls("Incoming calls", calls), nil
}

type OutgoingCallsTool struct {
	Manager engine.Manager
}


func (t OutgoingCallsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"code-outgoing_calls",
		"Find functions/methods called by the symbol at a 1-based file position. Use this to trace behavior through call chains.",
		positionSchema(),
	)
}

func (t OutgoingCallsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	pos, err := decodePosition(call.Arguments)
	if err != nil {
		return tool.Result{}, err
	}
	calls, err := t.Manager.OutgoingCalls(ctx, pos)
	if err != nil {
		return tool.Result{}, err
	}
	return formatCalls("Outgoing calls", calls), nil
}

type DiagnosticsTool struct {
	Manager engine.Manager
}


func (t DiagnosticsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"code-diagnostics",
		"Run language-server diagnostics for a file. Use this to check compile/type errors after reading or changing code.",
		tool.ObjectSchema([]string{"path"}, map[string]any{
			"repo": map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name."},
			"path": map[string]any{"type": "string", "description": "File path inside the repo."},
		}),
	)
}

func (t DiagnosticsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	diagnostics, err := t.Manager.Diagnostics(ctx, args.Repo, args.Path)
	if err != nil {
		return tool.Result{}, err
	}
	if len(diagnostics) == 0 {
		return tool.TextResult("no diagnostics"), nil
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
	return tool.TextResult(strings.TrimSpace(b.String())), nil
}

func positionSchema() map[string]any {
	return tool.ObjectSchema([]string{"path", "line", "character"}, map[string]any{
		"repo":      map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name."},
		"path":      map[string]any{"type": "string", "description": "File path inside the repo."},
		"line":      map[string]any{"type": "integer", "description": "1-based line number from code-read_file output."},
		"character": map[string]any{"type": "integer", "description": "1-based character offset."},
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

func formatLocations(title string, locations []engine.Location) tool.Result {
	if len(locations) == 0 {
		return tool.TextResult(strings.ToLower(title) + " not found")
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	for _, loc := range locations {
		b.WriteString(fmt.Sprintf("- %s:%d:%d\n", loc.Path, loc.Line, loc.Character))
	}
	return tool.TextResult(strings.TrimSpace(b.String()))
}

func formatCalls(title string, calls []engine.Call) tool.Result {
	if len(calls) == 0 {
		return tool.TextResult(strings.ToLower(title) + " not found")
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	for _, call := range calls {
		b.WriteString(fmt.Sprintf("- %s %s:%d:%d\n", call.Name, call.Path, call.Line, call.Character))
	}
	return tool.TextResult(strings.TrimSpace(b.String()))
}

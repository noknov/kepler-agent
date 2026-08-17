package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// NewSearchTool returns the deferred-tool discovery tool for a catalog.
func NewSearchTool(catalog *Catalog) Tool {
	return searchTool{catalog: catalog}
}

type searchTool struct {
	catalog *Catalog
}

func (searchTool) Descriptor() Descriptor {
	return FunctionDescriptor(
		"tool_search",
		"Capability router for tools that are intentionally not loaded by default to save context. Use action=search with query for discovery; use query=\"select:tool-a,tool-b\" or action=activate with tool_names/categories to load tools for subsequent steps.",
		ObjectSchema([]string{"action"}, map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"search", "list", "activate"},
				"description": "search: find tools by task or capability description; list: show deferred categories; activate: load requested tools or categories.",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Task, capability, service, or other search text. With action=search, prefix select: to activate exact tool names immediately.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum search results. Defaults to 8, max 20.",
			},
			"categories": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Category filters for activate.",
			},
			"tool_names": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Exact tool names to activate.",
			},
		}),
		WithEffects(EffectRead),
	)
}

func (t searchTool) Execute(_ context.Context, call Call) (Result, error) {
	var args struct {
		Action     string   `json:"action"`
		Query      string   `json:"query"`
		Limit      int      `json:"limit"`
		Categories []string `json:"categories"`
		ToolNames  []string `json:"tool_names"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	switch args.Action {
	case "list":
		return t.listDeferred()
	case "activate":
		return t.activate(call.Scope.SessionID, args.Categories, args.ToolNames)
	case "search":
		query := strings.TrimSpace(args.Query)
		if strings.HasPrefix(strings.ToLower(query), "select:") {
			names := splitList(query[len("select:"):])
			return t.activate(call.Scope.SessionID, nil, names)
		}
		return t.search(query, args.Limit)
	default:
		return Result{}, fmt.Errorf("action must be search, list, or activate")
	}
}

func (t searchTool) listDeferred() (Result, error) {
	lines := make([]string, 0)
	seen := map[string]bool{}
	for _, descriptor := range t.catalog.Descriptors() {
		if descriptor.Exposure != ExposureDeferred {
			continue
		}
		for _, tag := range descriptor.Tags {
			if seen[tag] {
				continue
			}
			seen[tag] = true
			line := tag
			if description := categoryDescriptions[tag]; description != "" {
				line += ": " + description
			}
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return TextResult("No deferred tools are available."), nil
	}
	sort.Strings(lines)
	return TextResult("Deferred categories:\n- " + strings.Join(lines, "\n- ")), nil
}

func (t searchTool) activate(sessionID string, categories, names []string) (Result, error) {
	wanted := map[string]bool{}
	for _, category := range categories {
		wanted[category] = true
	}
	for _, descriptor := range t.catalog.Descriptors() {
		if descriptor.Exposure != ExposureDeferred {
			continue
		}
		for _, tag := range descriptor.Tags {
			if wanted[tag] {
				names = append(names, descriptor.Name)
			}
		}
	}
	names = compactStrings(names)
	if len(names) == 0 {
		return TextResult("No tools requested."), nil
	}
	if err := t.catalog.Activate(sessionID, names...); err != nil {
		return Result{}, err
	}
	return TextResult("Activated tools:\n- " + strings.Join(names, "\n- ")), nil
}

func (t searchTool) search(query string, limit int) (Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Result{}, fmt.Errorf("query is required for search")
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > 20 {
		limit = 20
	}
	type scored struct {
		name  string
		score float64
		line  string
	}
	results := make([]scored, 0)
	for _, descriptor := range t.catalog.Descriptors() {
		if descriptor.Exposure != ExposureDeferred {
			continue
		}
		score, line := scoreDeferredTool(descriptor, query)
		if score <= 0 {
			continue
		}
		results = append(results, scored{name: descriptor.Name, score: score, line: line})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].score == results[j].score {
			return results[i].name < results[j].name
		}
		return results[i].score > results[j].score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	if len(results) == 0 {
		return TextResult("No deferred tools matched. Try action=list to see categories, then activate by category or exact tool name."), nil
	}
	lines := make([]string, 0, len(results))
	for _, result := range results {
		lines = append(lines, result.line)
	}
	return TextResult("Deferred tools matching \"" + query + "\":\n- " + strings.Join(lines, "\n- ")), nil
}

func scoreDeferredTool(descriptor Descriptor, query string) (float64, string) {
	tokens := tokenize(query)
	if len(tokens) == 0 {
		return 0, ""
	}
	corpus := strings.ToLower(strings.Join([]string{
		descriptor.Name,
		descriptor.Description,
		strings.Join(descriptor.Tags, " "),
		string(descriptor.InputSchema),
	}, " "))
	var score float64
	for _, token := range tokens {
		if strings.Contains(corpus, token) {
			score += 1
		}
		if strings.Contains(descriptor.Name, token) {
			score += 2
		}
		for _, tag := range descriptor.Tags {
			if strings.Contains(tag, token) {
				score += 1.5
			}
		}
	}
	if score == 0 {
		return 0, ""
	}
	tags := strings.Join(descriptor.Tags, ", ")
	return score, fmt.Sprintf("%s [%s] — %s", descriptor.Name, tags, trimDescription(descriptor.Description, 120))
}

func tokenize(value string) []string {
	fields := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) >= 2 {
			out = append(out, field)
		}
	}
	return out
}

func trimDescription(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}

func splitList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	})
	return compactStrings(parts)
}

func compactStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && (len(out) == 0 || out[len(out)-1] != value) {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

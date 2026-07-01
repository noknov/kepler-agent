package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/wati/oncall-agent/internal/llm"
)

var categoryDescriptions = map[string]string{
	CategoryDiagnostics:    "Incident investigation helpers: incident briefs, timelines, and evidence boards.",
	CategoryBrowser:        "Playwright browser automation: navigate, snapshot, click, type, screenshots, and page evaluation.",
	CategoryIntegration:    "External integrations: Notion, YouTrack, Luckin MCP, TTS, and related APIs.",
	CategoryInfrastructure: "Kubernetes cluster tools: get pods, describe resources, fetch logs, and check resource usage (kubectl top).",
}

type ToolSearchTool struct {
	Registry *Registry
}

func (ToolSearchTool) Spec() llm.ToolSpec {
	return FunctionSpec(
		"tool_search",
		"Discover active and deferred tools by task intent, then activate deferred tools when needed. Use action=search with query for tool discovery; use query=\"select:tool-a,tool-b\" or action=activate with tool_names/categories to load tools for subsequent steps.",
		ObjectSchema([]string{"action"}, map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"search", "list", "activate"},
				"description": "search: find tools by task intent; list: show deferred categories; activate: load requested tools or categories.",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Task, capability, service, or keyword to match against tool names, descriptions, parameters, and categories. With action=search, prefix select: to activate exact tool names immediately.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum search results. Defaults to 8, max 20.",
			},
			"categories": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
					"enum": []string{CategoryDiagnostics, CategoryBrowser, CategoryIntegration},
				},
				"description": "Deferred categories to activate. Optional with action=activate.",
			},
			"tool_names": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
				"description": "Specific deferred tool names to activate. Optional with action=activate.",
			},
		}),
	)
}

func (t ToolSearchTool) Execute(_ context.Context, raw json.RawMessage, _ Runtime) (Result, error) {
	if t.Registry == nil {
		return Result{}, fmt.Errorf("tool registry is not configured")
	}
	var args struct {
		Action     string   `json:"action"`
		Query      string   `json:"query"`
		Limit      int      `json:"limit"`
		Categories []string `json:"categories"`
		ToolNames  []string `json:"tool_names"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	switch strings.TrimSpace(args.Action) {
	case "search":
		if names := parseSelectQuery(args.Query); len(names) > 0 {
			return Result{Content: t.activate(nil, names)}, nil
		}
		return Result{Content: t.search(args.Query, args.Limit)}, nil
	case "list":
		return Result{Content: t.listCategories()}, nil
	case "activate":
		return Result{Content: t.activate(args.Categories, args.ToolNames)}, nil
	default:
		return Result{}, fmt.Errorf("action must be search, list, or activate")
	}
}

type toolSearchHit struct {
	name     string
	category string
	active   bool
	score    float64
	summary  string
}

func (t ToolSearchTool) search(query string, limit int) string {
	query = strings.TrimSpace(query)
	if limit <= 0 {
		limit = 8
	}
	if limit > 20 {
		limit = 20
	}
	hits := t.searchHits(query)
	if len(hits) == 0 {
		if query == "" {
			return "No tools found. Provide a query, or call action=list to inspect deferred categories."
		}
		return fmt.Sprintf("No tools matched %q. Call action=list to inspect deferred categories.", query)
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	var b strings.Builder
	if query == "" {
		b.WriteString("Available tools:\n")
	} else {
		b.WriteString(fmt.Sprintf("Tool matches for %q:\n", query))
	}
	for _, hit := range hits {
		state := "active"
		if !hit.active {
			state = "deferred"
		}
		if hit.category != "" {
			state += ", " + hit.category
		}
		b.WriteString(fmt.Sprintf("- %s [%s]: %s\n", hit.name, state, hit.summary))
	}
	hasActive := false
	hasDeferred := false
	for _, hit := range hits {
		if hit.active {
			hasActive = true
		} else {
			hasDeferred = true
		}
	}
	if hasActive {
		b.WriteString("\nActive tools ([active]) are ready to call by name — do NOT call tool_search again for them.")
	}
	if hasDeferred {
		b.WriteString("\nTo use deferred tools, call action=activate with tool_names or query=\"select:tool-a,tool-b\".")
	}
	return strings.TrimSpace(b.String())
}

func (t ToolSearchTool) searchHits(query string) []toolSearchHit {
	docs := t.toolSearchDocs()
	queryTerms := tokenizeSearchText(query)
	if strings.TrimSpace(query) == "" {
		hits := make([]toolSearchHit, 0, len(docs))
		for _, doc := range docs {
			if doc.name == "tool_search" {
				continue
			}
			hits = append(hits, toolSearchHit{
				name:     doc.name,
				category: doc.category,
				active:   doc.active,
				score:    1,
				summary:  summarizeTool(doc.spec),
			})
		}
		sortToolSearchHits(hits)
		return hits
	}

	bm25 := newBM25Index(docs)
	var hits []toolSearchHit
	for _, doc := range docs {
		if doc.name == "tool_search" {
			continue
		}
		score := bm25.score(doc, queryTerms) + exactToolBoost(doc, query)
		if score <= 0 {
			continue
		}
		hits = append(hits, toolSearchHit{
			name:     doc.name,
			category: doc.category,
			active:   doc.active,
			score:    score,
			summary:  summarizeTool(doc.spec),
		})
	}
	sortToolSearchHits(hits)
	return hits
}

type toolSearchDoc struct {
	name     string
	category string
	active   bool
	spec     llm.ToolSpec
	terms    []string
	tf       map[string]int
	length   int
}

func (t ToolSearchTool) toolSearchDocs() []toolSearchDoc {
	docs := make([]toolSearchDoc, 0, len(t.Registry.tools)+len(t.Registry.deferred))
	for name, tool := range t.Registry.tools {
		if !t.Registry.canExpose(name, tool) {
			continue
		}
		docs = append(docs, makeToolSearchDoc(name, "", true, tool.Spec()))
	}
	for name, tool := range t.Registry.deferred {
		if !t.Registry.canExpose(name, tool) {
			continue
		}
		spec := tool.Spec()
		category := ""
		if dt, ok := tool.(DeferredTool); ok {
			category = dt.Category()
		}
		docs = append(docs, makeToolSearchDoc(name, category, false, spec))
	}
	return docs
}

func makeToolSearchDoc(name, category string, active bool, spec llm.ToolSpec) toolSearchDoc {
	text := strings.Join([]string{
		name,
		strings.ReplaceAll(name, "-", " "),
		category,
		categoryDescriptions[category],
		spec.Function.Description,
		parameterText(spec.Function.Parameters),
	}, " ")
	terms := tokenizeSearchText(text)
	tf := make(map[string]int, len(terms))
	for _, term := range terms {
		tf[term]++
	}
	return toolSearchDoc{
		name:     name,
		category: category,
		active:   active,
		spec:     spec,
		terms:    terms,
		tf:       tf,
		length:   len(terms),
	}
}

type bm25Index struct {
	docs      []toolSearchDoc
	idf       map[string]float64
	avgLength float64
}

func newBM25Index(docs []toolSearchDoc) bm25Index {
	df := map[string]int{}
	totalLength := 0
	for _, doc := range docs {
		totalLength += doc.length
		seen := map[string]bool{}
		for _, term := range doc.terms {
			if seen[term] {
				continue
			}
			seen[term] = true
			df[term]++
		}
	}
	avgLength := 1.0
	if len(docs) > 0 {
		avgLength = float64(totalLength) / float64(len(docs))
		if avgLength <= 0 {
			avgLength = 1
		}
	}
	idf := make(map[string]float64, len(df))
	n := float64(len(docs))
	for term, freq := range df {
		idf[term] = math.Log(1 + (n-float64(freq)+0.5)/(float64(freq)+0.5))
	}
	return bm25Index{docs: docs, idf: idf, avgLength: avgLength}
}

func (idx bm25Index) score(doc toolSearchDoc, queryTerms []string) float64 {
	const (
		k1 = 1.2
		b  = 0.75
	)
	if len(queryTerms) == 0 || doc.length == 0 {
		return 0
	}
	score := 0.0
	seenQueryTerms := map[string]bool{}
	for _, term := range queryTerms {
		if seenQueryTerms[term] {
			continue
		}
		seenQueryTerms[term] = true
		freq := doc.tf[term]
		if freq == 0 {
			continue
		}
		tf := float64(freq)
		lengthNorm := 1 - b + b*(float64(doc.length)/idx.avgLength)
		score += idx.idf[term] * ((tf * (k1 + 1)) / (tf + k1*lengthNorm))
	}
	return score
}

func exactToolBoost(doc toolSearchDoc, query string) float64 {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return 0
	}
	name := strings.ToLower(doc.name)
	category := strings.ToLower(doc.category)
	score := 0.0
	if query == name {
		score += 8
	}
	if strings.Contains(name, query) {
		score += 4
	}
	for _, term := range tokenizeSearchText(query) {
		if strings.Contains(name, term) {
			score += 2
		}
		if category != "" && strings.Contains(category, term) {
			score += 1.5
		}
	}
	return score
}

func sortToolSearchHits(hits []toolSearchHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if hits[i].active != hits[j].active {
			return hits[i].active
		}
		return hits[i].name < hits[j].name
	})
}

func (t ToolSearchTool) listCategories() string {
	categories := t.Registry.DeferredCategories()
	if len(categories) == 0 {
		return "No deferred tool categories are available. All registered tools are already active."
	}
	var b strings.Builder
	b.WriteString("Deferred tool categories (call tool_search with action=activate to load):\n")
	for _, category := range categories {
		desc := categoryDescriptions[category]
		if desc == "" {
			desc = "Additional tools for specialized workflows."
		}
		tools := t.Registry.DeferredToolNames(category)
		b.WriteString(fmt.Sprintf("\n- %s (%d tools): %s\n", category, len(tools), desc))
		for _, name := range tools {
			b.WriteString("  - ")
			b.WriteString(name)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

func (t ToolSearchTool) activate(categories []string, toolNames []string) string {
	var activated []string
	var unknown []string
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if t.Registry.Has(name) {
			continue
		}
		if t.Registry.ActivateTool(name) {
			activated = append(activated, name)
		} else {
			unknown = append(unknown, name)
		}
	}
	for _, category := range categories {
		category = strings.TrimSpace(category)
		if category == "" {
			continue
		}
		if len(t.Registry.DeferredToolNames(category)) == 0 && !containsCategory(t.Registry.categories, category) {
			unknown = append(unknown, category)
			continue
		}
		activated = append(activated, t.Registry.ActivateCategory(category)...)
	}
	var b strings.Builder
	if len(activated) > 0 {
		sort.Strings(activated)
		b.WriteString(fmt.Sprintf("Activated %d tools:\n", len(activated)))
		for _, name := range activated {
			b.WriteString("- ")
			b.WriteString(name)
			b.WriteByte('\n')
		}
		b.WriteString("\nThese tools are now available on subsequent steps.")
	} else if len(unknown) > 0 {
		b.WriteString("No tools activated. Unknown tools or categories: ")
		b.WriteString(strings.Join(unknown, ", "))
	} else {
		b.WriteString("No tools activated. Requested tools/categories are already active, or no tools/categories were requested.")
	}
	return strings.TrimSpace(b.String())
}

func containsCategory(categories map[string][]string, category string) bool {
	_, ok := categories[category]
	return ok
}

func parseSelectQuery(query string) []string {
	query = strings.TrimSpace(query)
	if len(query) < len("select:") || !strings.EqualFold(query[:len("select:")], "select:") {
		return nil
	}
	raw := strings.TrimSpace(query[len("select:"):])
	if raw == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\t' || r == ' '
	})
	names := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, field := range fields {
		name := strings.TrimSpace(field)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func tokenizeSearchText(text string) []string {
	text = strings.ToLower(splitSearchTokenBoundaries(text))
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		terms = append(terms, field)
	}
	return terms
}

func splitSearchTokenBoundaries(text string) string {
	var b strings.Builder
	var prev rune
	for _, r := range text {
		if prev != 0 && unicode.IsLower(prev) && unicode.IsUpper(r) {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

func summarizeTool(spec llm.ToolSpec) string {
	desc := strings.TrimSpace(spec.Function.Description)
	if desc == "" {
		return "No description available."
	}
	desc = strings.Join(strings.Fields(desc), " ")
	runes := []rune(desc)
	if len(runes) > 180 {
		return string(runes[:180]) + "..."
	}
	return desc
}

func parameterText(value any) string {
	switch v := value.(type) {
	case map[string]any:
		parts := make([]string, 0, len(v))
		for key, child := range v {
			parts = append(parts, key, parameterText(child))
		}
		return strings.Join(parts, " ")
	case []any:
		parts := make([]string, 0, len(v))
		for _, child := range v {
			parts = append(parts, parameterText(child))
		}
		return strings.Join(parts, " ")
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

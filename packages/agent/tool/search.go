package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/noknov/kepler-agent/packages/agent/model"
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
		"Discover tools that are not loaded by default to save context. Use action=search with query for discovery; use action=activate with exact tool_names to load tools for subsequent steps. Search never activates tools.",
		ObjectSchema([]string{"action"}, map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"search", "list", "activate"},
				"description": "search: find tools by task or capability description; list: show deferred categories; activate: load exact tool names.",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Capability keywords, service names, or an exact registered tool identifier to search for.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum search results. Defaults to 8, max 20.",
			},
			"categories": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Category filters for search; never activates a whole category.",
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

func (t searchTool) Execute(ctx context.Context, call Call) (Result, error) {
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
		if len(args.Categories) > 0 {
			return Result{}, fmt.Errorf("categories filter search results; activate requires exact tool_names")
		}
		return t.activate(ctx, call, args.ToolNames)
	case "search":
		return t.search(args.Query, args.Limit, args.Categories...)
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

func (t searchTool) activate(ctx context.Context, call Call, names []string) (Result, error) {
	names = compactStrings(names)
	if len(names) == 0 {
		return Result{}, fmt.Errorf("tool_names is required to activate tools")
	}
	result := TextResult("Activated tools:\n- " + strings.Join(names, "\n- "))
	if err := t.catalog.activateForCall(ctx, call, names, result); err != nil {
		return Result{Content: []model.Content{{Type: model.ContentText, Text: "Tool activation rejected; the active tools are unchanged. Request fewer exact tool names. " + err.Error()}}, IsError: true, ErrorCode: "tool_activation_rejected"}, nil
	}
	return result, nil
}

func (t searchTool) search(query string, limit int, categories ...string) (Result, error) {
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
		candidate *candidateDoc
		exact     bool
		score     float64
	}
	index := t.catalog.searchIndexFor(categories)
	candidates, docFreq, totalDocLen := index.candidates, index.docFreq, index.totalDocLen
	avgDocLen := 1.0
	if len(candidates) > 0 && totalDocLen > 0 {
		avgDocLen = float64(totalDocLen) / float64(len(candidates))
	}
	tokens := queryTokens(query)
	results := make([]scored, 0, len(candidates))
	for i := range candidates {
		candidate := &candidates[i]
		exact := candidate.descriptor.Name == query
		score := scoreBM25Candidate(*candidate, avgDocLen, len(candidates), docFreq, tokens)
		if !exact && score <= 0 {
			continue
		}
		results = append(results, scored{candidate: candidate, exact: exact, score: score})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].exact != results[j].exact {
			return results[i].exact
		}

		if results[i].score == results[j].score {
			return results[i].candidate.descriptor.Name < results[j].candidate.descriptor.Name
		}
		return results[i].score > results[j].score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	if len(results) == 0 {
		return TextResult("No deferred tools matched. Try action=list to see categories, then search within a category and activate exact tool names."), nil
	}
	lines := make([]string, 0, len(results))
	for _, result := range results {
		descriptor := result.candidate.descriptor
		lines = append(lines, fmt.Sprintf("%s [%s] — %s", descriptor.Name, strings.Join(descriptor.Tags, ", "), trimDescription(descriptor.Description, 120)))
	}
	return TextResult("Deferred tools matching \"" + query + "\":\n- " + strings.Join(lines, "\n- ")), nil
}

func scoreBM25Candidate(
	candidate candidateDoc,
	avgDocLen float64,
	totalDocs int,
	docFreq map[string]int,
	tokens []string,
) float64 {
	const (
		k1 = 1.2
		b  = 0.75
	)
	var score float64
	for _, token := range tokens {
		tf := float64(candidate.terms[token])
		if tf == 0 {
			continue
		}
		df := float64(docFreq[token])
		idf := math.Log(1.0 + (float64(totalDocs)-df+0.5)/(df+0.5))
		norm := tf * (k1 + 1.0) / (tf + k1*(1.0-b+b*(float64(candidate.docLen)/avgDocLen)))
		score += idf * norm
	}
	return score
}

func termFrequencies(value string) map[string]int {
	tokens := tokenize(value)
	freqs := make(map[string]int, len(tokens))
	for _, token := range tokens {
		freqs[token]++
	}
	return freqs
}

func queryTokens(query string) []string {
	return compactStrings(tokenize(query))
}

func isCJKRune(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

func tokenize(value string) []string {
	var out []string
	var asciiBuf []rune
	var cjkBuf []rune

	flushASCII := func() {
		if len(asciiBuf) >= 2 {
			full := strings.ToLower(string(asciiBuf))
			out = append(out, full)
			for _, part := range splitCamelCase(asciiBuf) {
				if len(part) >= 2 && part != full {
					out = append(out, part)
				}
			}
		}
		asciiBuf = asciiBuf[:0]
	}
	flushCJK := func() {
		n := len(cjkBuf)
		if n == 1 {
			out = append(out, string(cjkBuf))
		} else if n >= 2 {
			for i := 0; i < n-1; i++ {
				out = append(out, string(cjkBuf[i:i+2]))
			}
			for i := 0; i < n-2; i++ {
				out = append(out, string(cjkBuf[i:i+3]))
			}
		}
		cjkBuf = cjkBuf[:0]
	}

	for _, r := range value {
		switch {
		case isCJKRune(r):
			flushASCII()
			cjkBuf = append(cjkBuf, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			asciiBuf = append(asciiBuf, r)
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return out
}

func splitCamelCase(runes []rune) []string {
	if len(runes) == 0 {
		return nil
	}
	var parts []string
	start := 0
	for i := 1; i < len(runes); i++ {
		prev := runes[i-1]
		curr := runes[i]
		nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
		if (unicode.IsLower(prev) && unicode.IsUpper(curr)) ||
			(unicode.IsLetter(prev) && unicode.IsDigit(curr)) ||
			(unicode.IsDigit(prev) && unicode.IsLetter(curr)) ||
			(unicode.IsUpper(prev) && unicode.IsUpper(curr) && nextLower) {
			parts = append(parts, strings.ToLower(string(runes[start:i])))
			start = i
		}
	}
	parts = append(parts, strings.ToLower(string(runes[start:])))
	return parts
}

func trimDescription(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= max {
		return value
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "..."
}

func compactStrings(values []string) []string {
	out := values[:0]
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func matchesCategory(tags, categories []string) bool {
	for _, tag := range tags {
		for _, category := range categories {
			if tag == category {
				return true
			}
		}
	}
	return false
}

type candidateDoc struct {
	descriptor Descriptor
	terms      map[string]int
	docLen     int
}

type searchIndex struct {
	candidates  []candidateDoc
	docFreq     map[string]int
	totalDocLen int
}

func (c *Catalog) searchIndexFor(categories []string) *searchIndex {
	categories = compactStrings(append([]string(nil), categories...))
	key := strings.Join(categories, "\x00")
	c.mu.Lock()
	defer c.mu.Unlock()
	if index := c.searchIndexes[key]; index != nil {
		return index
	}
	descriptors := make([]Descriptor, 0, len(c.tools))
	for _, item := range c.tools {
		descriptors = append(descriptors, item.Descriptor())
	}
	candidates := make([]candidateDoc, 0)
	docFreq := make(map[string]int)
	totalDocLen := 0
	for _, descriptor := range descriptors {
		if descriptor.Exposure != ExposureDeferred {
			continue
		}
		if len(categories) > 0 && !matchesCategory(descriptor.Tags, categories) {
			continue
		}
		terms := termFrequencies(descriptor.Name + " " + strings.Join(descriptor.Tags, " ") + " " + descriptor.Description + " " + string(descriptor.InputSchema))
		docLen := 0
		for term, count := range terms {
			docLen += count
			docFreq[term]++
		}
		totalDocLen += docLen
		candidates = append(candidates, candidateDoc{
			descriptor: descriptor,
			terms:      terms,
			docLen:     docLen,
		})
	}

	index := &searchIndex{candidates: candidates, docFreq: docFreq, totalDocLen: totalDocLen}
	if c.searchIndexes == nil {
		c.searchIndexes = make(map[string]*searchIndex)
	}
	if len(c.searchIndexes) >= 32 {
		c.searchIndexes = make(map[string]*searchIndex)
	}
	c.searchIndexes[key] = index
	return index
}

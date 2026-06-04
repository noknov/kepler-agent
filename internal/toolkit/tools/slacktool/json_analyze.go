package slacktool

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/slack"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type JSONAnalyzeTool struct {
	Slack FileSearcher
}

func (t JSONAnalyzeTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"slack-json_analyze",
		"Analyze an uploaded Slack JSON file structurally without putting the full file into model context. Use this for JSON statistics, field distributions, numeric summaries, top values, and group-by analysis.",
		registry.ObjectSchema([]string{"file_id"}, map[string]any{
			"file_id":  map[string]any{"type": "string", "description": "Slack file id from uploaded file metadata, e.g. F012ABCDEF."},
			"group_by": map[string]any{"type": "string", "description": "Optional top-level or dotted field path to group records by."},
			"metrics": map[string]any{
				"type":        "array",
				"description": "Optional numeric field paths to summarize. When omitted, numeric fields are detected automatically.",
				"items":       map[string]any{"type": "string"},
			},
			"top_fields": map[string]any{
				"type":        "array",
				"description": "Optional categorical field paths for top value counts. When omitted, low-cardinality string/bool fields are detected automatically.",
				"items":       map[string]any{"type": "string"},
			},
			"limit": map[string]any{"type": "integer", "description": "Maximum top values or groups to return. Defaults to 10, max 50."},
		}),
	)
}

func (t JSONAnalyzeTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	if t.Slack == nil {
		return registry.Result{}, fmt.Errorf("Slack JSON analysis is not configured")
	}
	var args struct {
		FileID    string   `json:"file_id"`
		GroupBy   string   `json:"group_by"`
		Metrics   []string `json:"metrics"`
		TopFields []string `json:"top_fields"`
		Limit     int      `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	args.FileID = strings.TrimSpace(args.FileID)
	args.GroupBy = strings.TrimSpace(args.GroupBy)
	if args.FileID == "" {
		return registry.Result{}, fmt.Errorf("file_id is required")
	}
	if args.Limit <= 0 {
		args.Limit = 10
	}
	if args.Limit > 50 {
		args.Limit = 50
	}
	file, err := t.Slack.FileInfo(ctx, args.FileID)
	if err != nil {
		return registry.Result{}, err
	}
	if !isJSONFile(file) {
		return registry.Result{}, fmt.Errorf("file does not look like JSON: %s", slack.FileDisplayName(file))
	}
	if file.Size > maxSearchFileBytes {
		return registry.Result{}, fmt.Errorf("file exceeds analyzable size %s", formatBytes(maxSearchFileBytes))
	}
	data, err := t.Slack.DownloadFile(ctx, file, maxSearchFileBytes)
	if err != nil {
		return registry.Result{}, err
	}
	records, shape, err := parseJSONRecords(data)
	if err != nil {
		return registry.Result{}, err
	}
	report := analyzeRecords(records, shape, args.GroupBy, args.Metrics, args.TopFields, args.Limit)
	var b strings.Builder
	b.WriteString("JSON analysis\n")
	b.WriteString("file: " + slack.FileDisplayName(file) + "\n")
	b.WriteString("id: " + args.FileID + "\n")
	b.WriteString(report)
	return registry.Result{Content: strings.TrimSpace(b.String())}, nil
}

func isJSONFile(file slack.File) bool {
	mime := strings.ToLower(strings.TrimSpace(file.Mimetype))
	if mime == "application/json" || strings.HasSuffix(mime, "+json") {
		return true
	}
	filetype := strings.ToLower(strings.TrimSpace(file.Filetype))
	if filetype == "json" {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(firstNonEmpty(file.Name, file.Title)))
	return strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".jsonl")
}

func parseJSONRecords(data []byte) ([]map[string]any, string, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return parseJSONLines(data)
	}
	switch v := value.(type) {
	case []any:
		records := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if obj, ok := item.(map[string]any); ok {
				records = append(records, obj)
			} else {
				records = append(records, map[string]any{"value": item})
			}
		}
		return records, "array", nil
	case map[string]any:
		if arr, ok := firstArrayOfObjects(v); ok {
			return arr, "object_with_array", nil
		}
		return []map[string]any{v}, "object", nil
	default:
		return []map[string]any{{"value": v}}, "scalar", nil
	}
}

func parseJSONLines(data []byte) ([]map[string]any, string, error) {
	lines := strings.Split(string(data), "\n")
	records := make([]map[string]any, 0, len(lines))
	for idx, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var value any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			return nil, "", fmt.Errorf("parse json/jsonl line %d: %w", idx+1, err)
		}
		if obj, ok := value.(map[string]any); ok {
			records = append(records, obj)
		} else {
			records = append(records, map[string]any{"value": value})
		}
	}
	if len(records) == 0 {
		return nil, "", fmt.Errorf("json file contains no records")
	}
	return records, "jsonl", nil
}

func firstArrayOfObjects(obj map[string]any) ([]map[string]any, bool) {
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		arr, ok := obj[key].([]any)
		if !ok || len(arr) == 0 {
			continue
		}
		records := make([]map[string]any, 0, len(arr))
		for _, item := range arr {
			record, ok := item.(map[string]any)
			if !ok {
				records = nil
				break
			}
			records = append(records, record)
		}
		if len(records) > 0 {
			return records, true
		}
	}
	return nil, false
}

func analyzeRecords(records []map[string]any, shape, groupBy string, metrics, topFields []string, limit int) string {
	stats := collectFieldStats(records)
	if len(metrics) == 0 {
		metrics = autoNumericFields(stats)
	}
	if len(topFields) == 0 {
		topFields = autoCategoricalFields(stats)
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("\nshape: %s\nrecords: %d\n", shape, len(records)))
	b.WriteString("\nFields\n")
	for _, name := range sortedFieldNames(stats) {
		st := stats[name]
		b.WriteString(fmt.Sprintf("- %s: types=%s present=%d null=%d missing=%d", name, formatCounts(st.Types), st.Present, st.Nulls, len(records)-st.Present))
		if st.Number.Count > 0 {
			b.WriteString(fmt.Sprintf(" numeric_count=%d min=%.4g max=%.4g avg=%.4g sum=%.4g", st.Number.Count, st.Number.Min, st.Number.Max, st.Number.Avg(), st.Number.Sum))
		}
		b.WriteString("\n")
	}
	writeNumericSummaries(&b, "Numeric summaries", stats, metrics)
	writeTopValues(&b, "Top values", stats, topFields, limit)
	if groupBy != "" {
		writeGroups(&b, records, groupBy, metrics, limit)
	}
	return b.String()
}

type fieldStats struct {
	Present int
	Nulls   int
	Types   map[string]int
	Number  numberStats
	Values  map[string]int
}

type numberStats struct {
	Count int
	Sum   float64
	Min   float64
	Max   float64
}

func (s numberStats) Avg() float64 {
	if s.Count == 0 {
		return 0
	}
	return s.Sum / float64(s.Count)
}

func collectFieldStats(records []map[string]any) map[string]*fieldStats {
	out := map[string]*fieldStats{}
	for _, record := range records {
		flat := map[string]any{}
		flatten("", record, flat)
		for path, value := range flat {
			st := out[path]
			if st == nil {
				st = &fieldStats{Types: map[string]int{}, Values: map[string]int{}, Number: numberStats{Min: math.Inf(1), Max: math.Inf(-1)}}
				out[path] = st
			}
			st.Present++
			kind := valueKind(value)
			st.Types[kind]++
			if value == nil {
				st.Nulls++
				continue
			}
			if num, ok := asFloat(value); ok {
				st.Number.Count++
				st.Number.Sum += num
				if num < st.Number.Min {
					st.Number.Min = num
				}
				if num > st.Number.Max {
					st.Number.Max = num
				}
			}
			if kind == "string" || kind == "bool" || kind == "number" {
				key := fmt.Sprintf("%v", value)
				if len(key) > 120 {
					key = key[:120] + "...[truncated]"
				}
				st.Values[key]++
			}
		}
	}
	return out
}

func flatten(prefix string, value any, out map[string]any) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			flatten(path, child, out)
		}
	default:
		if prefix == "" {
			prefix = "value"
		}
		out[prefix] = value
	}
}

func valueKind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func asFloat(value any) (float64, bool) {
	num, ok := value.(float64)
	return num, ok
}

func sortedFieldNames(stats map[string]*fieldStats) []string {
	names := make([]string, 0, len(stats))
	for name := range stats {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func formatCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", key, counts[key]))
	}
	return strings.Join(parts, ",")
}

func autoNumericFields(stats map[string]*fieldStats) []string {
	fields := make([]string, 0)
	for name, st := range stats {
		if st.Number.Count > 0 {
			fields = append(fields, name)
		}
	}
	sort.Strings(fields)
	if len(fields) > 20 {
		fields = fields[:20]
	}
	return fields
}

func autoCategoricalFields(stats map[string]*fieldStats) []string {
	fields := make([]string, 0)
	for name, st := range stats {
		if len(st.Values) > 0 && len(st.Values) <= 100 {
			fields = append(fields, name)
		}
	}
	sort.Strings(fields)
	if len(fields) > 20 {
		fields = fields[:20]
	}
	return fields
}

func writeNumericSummaries(b *strings.Builder, title string, stats map[string]*fieldStats, fields []string) {
	b.WriteString("\n" + title + "\n")
	if len(fields) == 0 {
		b.WriteString("- none\n")
		return
	}
	for _, field := range fields {
		st := stats[field]
		if st == nil || st.Number.Count == 0 {
			continue
		}
		b.WriteString(fmt.Sprintf("- %s: count=%d min=%.4g max=%.4g avg=%.4g sum=%.4g\n", field, st.Number.Count, st.Number.Min, st.Number.Max, st.Number.Avg(), st.Number.Sum))
	}
}

func writeTopValues(b *strings.Builder, title string, stats map[string]*fieldStats, fields []string, limit int) {
	b.WriteString("\n" + title + "\n")
	if len(fields) == 0 {
		b.WriteString("- none\n")
		return
	}
	for _, field := range fields {
		st := stats[field]
		if st == nil || len(st.Values) == 0 {
			continue
		}
		b.WriteString("- " + field + ":\n")
		for _, item := range topCounts(st.Values, limit) {
			b.WriteString(fmt.Sprintf("  - %s: %d\n", item.Key, item.Count))
		}
	}
}

func writeGroups(b *strings.Builder, records []map[string]any, groupBy string, metrics []string, limit int) {
	type group struct {
		Count   int
		Metrics map[string]numberStats
	}
	groups := map[string]*group{}
	for _, record := range records {
		flat := map[string]any{}
		flatten("", record, flat)
		key := fmt.Sprintf("%v", flat[groupBy])
		if key == "<nil>" {
			key = "(missing)"
		}
		g := groups[key]
		if g == nil {
			g = &group{Metrics: map[string]numberStats{}}
			groups[key] = g
		}
		g.Count++
		for _, metric := range metrics {
			num, ok := asFloat(flat[metric])
			if !ok {
				continue
			}
			ns := g.Metrics[metric]
			if ns.Count == 0 {
				ns.Min = num
				ns.Max = num
			}
			ns.Count++
			ns.Sum += num
			if num < ns.Min {
				ns.Min = num
			}
			if num > ns.Max {
				ns.Max = num
			}
			g.Metrics[metric] = ns
		}
	}
	counts := map[string]int{}
	for key, group := range groups {
		counts[key] = group.Count
	}
	b.WriteString("\nGroups by " + groupBy + "\n")
	for _, item := range topCounts(counts, limit) {
		group := groups[item.Key]
		b.WriteString(fmt.Sprintf("- %s: count=%d", item.Key, group.Count))
		for _, metric := range metrics {
			ns := group.Metrics[metric]
			if ns.Count > 0 {
				b.WriteString(fmt.Sprintf(" %s_sum=%.4g %s_avg=%.4g", metric, ns.Sum, metric, ns.Avg()))
			}
		}
		b.WriteString("\n")
	}
}

type countItem struct {
	Key   string
	Count int
}

func topCounts(counts map[string]int, limit int) []countItem {
	items := make([]countItem, 0, len(counts))
	for key, count := range counts {
		items = append(items, countItem{Key: key, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Key < items[j].Key
		}
		return items[i].Count > items[j].Count
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

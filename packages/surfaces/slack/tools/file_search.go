package slacktool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

const (
	maxSearchFileBytes = 16 << 20
	maxSearchTextChars = 2_000_000
)

type FileSearcher interface {
	FileInfo(ctx context.Context, fileID string) (slack.File, error)
	DownloadFile(ctx context.Context, file slack.File, maxBytes int64) ([]byte, error)
}

type FileSearchTool struct {
	Source FileSearcherSource
	Slack  FileSearcher
}

func (t FileSearchTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"slack-file_search",
		"",
		tool.ObjectSchema([]string{"file_id"}, map[string]any{
			"file_id": map[string]any{"type": "string", "description": ""},
			"query":   map[string]any{"type": "string", "description": ""},
			"limit":   map[string]any{"type": "integer", "description": ""},
		}),
		readNetwork()...,
	)
}

func (t FileSearchTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	searcher, early, err := beginFileSearch(ctx, t.Source, t.Slack, call)
	if early != nil || err != nil {
		if early != nil {
			return *early, err
		}
		return tool.Result{}, err
	}
	if searcher == nil {
		searcher = t.Slack
	}
	if searcher == nil {
		return tool.Result{}, fmt.Errorf("Slack file search is not configured")
	}
	var args struct {
		FileID string `json:"file_id"`
		Query  string `json:"query"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	args.FileID = strings.TrimSpace(args.FileID)
	args.Query = strings.TrimSpace(args.Query)
	if args.FileID == "" {
		return tool.Result{}, fmt.Errorf("file_id is required")
	}
	if args.Limit <= 0 {
		args.Limit = 5
	}
	if args.Limit > 20 {
		args.Limit = 20
	}
	file, err := searcher.FileInfo(ctx, args.FileID)
	if err != nil {
		return tool.Result{}, err
	}
	text, err := t.extract(ctx, searcher, file)
	if err != nil {
		return tool.Result{}, err
	}
	excerpts := searchText(text, args.Query, args.Limit)
	if len(excerpts) == 0 {
		return tool.TextResult("no matching excerpts in " + slack.FileDisplayName(file)), nil
	}
	var b strings.Builder
	b.WriteString("Slack file excerpts\n")
	b.WriteString("file: " + slack.FileDisplayName(file) + "\n")
	b.WriteString("id: " + args.FileID + "\n")
	if args.Query != "" {
		b.WriteString("query: " + args.Query + "\n")
	}
	for i, excerpt := range excerpts {
		b.WriteString(fmt.Sprintf("\nExcerpt %d:\n%s\n", i+1, excerpt))
	}
	return tool.TextResult(strings.TrimSpace(b.String())), nil
}

func (t FileSearchTool) extract(ctx context.Context, searcher FileSearcher, file slack.File) (string, error) {
	if file.Size > maxSearchFileBytes {
		return "", fmt.Errorf("file exceeds searchable size %s", formatBytes(maxSearchFileBytes))
	}
	data, err := searcher.DownloadFile(ctx, file, maxSearchFileBytes)
	if err != nil {
		return "", err
	}
	if slack.IsPDFFile(file) {
		return slack.ExtractPDFText(data, maxSearchTextChars)
	}
	if !slack.IsTextFile(file) {
		return "", fmt.Errorf("unsupported file type for search: %s", firstNonEmpty(file.Mimetype, file.Filetype, "unknown"))
	}
	return slack.ExtractTextFile(data, maxSearchTextChars)
}

func searchText(text, query string, limit int) []string {
	if strings.TrimSpace(query) == "" {
		return []string{trimExcerpt(text, 0, 2400)}
	}
	lower := strings.ToLower(text)
	terms := strings.Fields(strings.ToLower(query))
	positions := make([]int, 0)
	for _, term := range terms {
		start := 0
		for {
			idx := strings.Index(lower[start:], term)
			if idx < 0 {
				break
			}
			positions = append(positions, start+idx)
			start += idx + len(term)
		}
	}
	if len(positions) == 0 && strings.TrimSpace(query) != "" {
		idx := strings.Index(lower, strings.ToLower(query))
		if idx >= 0 {
			positions = append(positions, idx)
		}
	}
	sort.Ints(positions)
	out := make([]string, 0, limit)
	last := -1
	for _, pos := range positions {
		if last >= 0 && pos-last < 900 {
			continue
		}
		out = append(out, trimExcerpt(text, pos, 2400))
		last = pos
		if len(out) >= limit {
			break
		}
	}
	return out
}

func trimExcerpt(text string, center, max int) string {
	if len(text) <= max {
		return strings.TrimSpace(text)
	}
	start := center - max/3
	if start < 0 {
		start = 0
	}
	end := start + max
	if end > len(text) {
		end = len(text)
		start = end - max
		if start < 0 {
			start = 0
		}
	}
	prefix := ""
	if start > 0 {
		prefix = "...[truncated]\n"
	}
	suffix := ""
	if end < len(text) {
		suffix = "\n...[truncated]"
	}
	return prefix + strings.TrimSpace(text[start:end]) + suffix
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.0f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

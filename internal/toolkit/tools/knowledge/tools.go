package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/prompts"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type RunbookSearchTool struct {
	Dir string
}

func (RunbookSearchTool) Parallel() bool { return true }

func (t RunbookSearchTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"knowledge-runbook_search",
		"Search local service runbooks under PROMPT_DIR/runbooks. Use this for service ownership, known alerts, dashboards, playbooks, and recurring incident procedures.",
		registry.ObjectSchema([]string{"query"}, map[string]any{
			"query": map[string]any{"type": "string", "description": "Service, alert, symptom, dashboard, or runbook keyword."},
			"limit": map[string]any{"type": "integer", "description": "Maximum runbook matches. Defaults to 5, max 20."},
		}),
	)
}

func (t RunbookSearchTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return registry.Result{}, fmt.Errorf("query is required")
	}
	if args.Limit <= 0 {
		args.Limit = 5
	}
	if args.Limit > 20 {
		args.Limit = 20
	}
	dir := t.Dir
	if dir == "" {
		dir = filepath.Join(prompts.Dir(), "runbooks")
	}
	matches, err := searchRunbooks(ctx, dir, query, args.Limit)
	if err != nil {
		return registry.Result{}, err
	}
	if len(matches) == 0 {
		return registry.Result{Content: "no matching runbooks"}, nil
	}
	var b strings.Builder
	b.WriteString("Runbook matches\n")
	for _, match := range matches {
		b.WriteString("- " + match.Name + " score=" + fmt.Sprintf("%d", match.Score) + "\n")
		b.WriteString(indent(excerpt(match.Content, query), "  ") + "\n")
	}
	return registry.Result{Content: strings.TrimSpace(b.String())}, nil
}

type runbookMatch struct {
	Name    string
	Content string
	Score   int
}

func searchRunbooks(ctx context.Context, dir, query string, limit int) ([]runbookMatch, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	terms := strings.Fields(strings.ToLower(query))
	matches := make([]runbookMatch, 0)
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		content := string(data)
		score := scoreText(strings.ToLower(entry.Name()+"\n"+content), terms)
		if score == 0 {
			continue
		}
		matches = append(matches, runbookMatch{Name: entry.Name(), Content: content, Score: score})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score == matches[j].Score {
			return matches[i].Name < matches[j].Name
		}
		return matches[i].Score > matches[j].Score
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func scoreText(text string, terms []string) int {
	score := 0
	for _, term := range terms {
		if term == "" {
			continue
		}
		score += strings.Count(text, term)
	}
	return score
}

func excerpt(content, query string) string {
	content = strings.TrimSpace(content)
	if len(content) <= 1200 {
		return content
	}
	lower := strings.ToLower(content)
	pos := -1
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if idx := strings.Index(lower, term); idx >= 0 && (pos == -1 || idx < pos) {
			pos = idx
		}
	}
	if pos < 0 {
		return strings.TrimSpace(content[:1200]) + "\n...[truncated]"
	}
	start := pos - 400
	if start < 0 {
		start = 0
	}
	end := start + 1200
	if end > len(content) {
		end = len(content)
	}
	prefix := ""
	if start > 0 {
		prefix = "...[truncated]\n"
	}
	suffix := ""
	if end < len(content) {
		suffix = "\n...[truncated]"
	}
	return prefix + strings.TrimSpace(content[start:end]) + suffix
}

func indent(text, prefix string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

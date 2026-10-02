// Package environment builds ephemeral runtime context for model requests.
package environment

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

const MessageID = "environment:context"

// Config renders a Codex-style <environment_context> user fragment.
type Config struct {
	WorkspaceRoots []string
	Now            func() time.Time
}

func (c Config) Message() model.Message {
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now()
	}
	body := render(normalizedRoots(c.WorkspaceRoots), now)
	return model.Message{
		ID:      MessageID,
		Role:    model.RoleUser,
		Content: []model.Content{{Type: model.ContentText, Text: body}},
	}
}

func normalizedRoots(roots []string) []string {
	seen := make(map[string]bool, len(roots))
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
		root = filepath.Clean(root)
		if seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	sort.Strings(out)
	return out
}

func render(roots []string, now time.Time) string {
	zone := strings.TrimSpace(now.Location().String())
	if zone == "" {
		zone = "UTC"
	}
	var body strings.Builder
	body.WriteString("<environment_context>\n")
	writeTag(&body, "current_date", now.Format("2006-01-02"))
	writeTag(&body, "timezone", zone)
	if len(roots) > 0 {
		body.WriteString("  <workspace_roots>\n")
		for _, root := range roots {
			body.WriteString("    <root>")
			body.WriteString(xmlEscape(root))
			body.WriteString("</root>\n")
		}
		body.WriteString("  </workspace_roots>\n")
	}
	body.WriteString("</environment_context>")
	return body.String()
}

func writeTag(body *strings.Builder, name, value string) {
	body.WriteString("  <")
	body.WriteString(name)
	body.WriteString(">")
	body.WriteString(xmlEscape(value))
	body.WriteString("</")
	body.WriteString(name)
	body.WriteString(">\n")
}

func xmlEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

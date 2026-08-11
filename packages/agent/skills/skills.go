// Package skills discovers file-backed instruction skills and exposes their
// bodies only when the model explicitly loads one.
package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type Skill struct{ Name, Description, Path string }
type Catalog struct{ items map[string]Skill }

func Discover(roots []string) (*Catalog, error) {
	catalog := &Catalog{items: make(map[string]Skill)}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(root, entry.Name(), "SKILL.md")
			data, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			name, description := metadata(entry.Name(), string(data))
			key := strings.ToLower(name)
			if _, exists := catalog.items[key]; exists {
				return nil, fmt.Errorf("duplicate skill %q", name)
			}
			catalog.items[key] = Skill{Name: name, Description: description, Path: path}
		}
	}
	return catalog, nil
}

func (c *Catalog) Prompt() string {
	if c == nil || len(c.items) == 0 {
		return ""
	}
	items := make([]Skill, 0, len(c.items))
	for _, item := range c.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	var value strings.Builder
	value.WriteString("Available file skills. Load a matching skill with skill_load before following it:\n")
	for _, item := range items {
		fmt.Fprintf(&value, "- %s: %s\n", item.Name, item.Description)
	}
	return strings.TrimSpace(value.String())
}

func (c *Catalog) Tool() tool.Tool { return loadTool{catalog: c} }

type loadTool struct{ catalog *Catalog }

func (loadTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "skill_load", Description: "Load the complete instructions for an available file skill.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"name":{"type":"string"}},"required":["name"]}`), Effects: []tool.Effect{tool.EffectRead}, Exposure: tool.ExposureEager}
}
func (t loadTool) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	var arguments struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	item, ok := t.catalog.items[strings.ToLower(arguments.Name)]
	if !ok {
		return tool.Result{}, fmt.Errorf("unknown skill %q", arguments.Name)
	}
	data, err := os.ReadFile(item.Path)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(string(data)), nil
}

func metadata(fallback, body string) (string, string) {
	name, description := fallback, ""
	if !strings.HasPrefix(body, "---\n") {
		return name, description
	}
	end := strings.Index(body[4:], "\n---")
	if end < 0 {
		return name, description
	}
	for _, line := range strings.Split(body[4:4+end], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.TrimSpace(key) {
		case "name":
			if value != "" {
				name = value
			}
		case "description":
			description = value
		}
	}
	return name, description
}

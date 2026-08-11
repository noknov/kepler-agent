package localtools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/local"
	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

func NewCatalog(workspace local.Workspace, sandbox local.Sandbox) (*tool.Catalog, error) {
	catalog, err := tool.NewCatalog(
		ReadFile{Workspace: workspace}, ListFiles{Workspace: workspace}, Search{Workspace: workspace},
		WriteFile{Workspace: workspace}, EditFile{Workspace: workspace}, Shell{Sandbox: sandbox},
	)
	if err != nil {
		return nil, err
	}
	if err := catalog.Register(ToolSearch{Catalog: catalog}); err != nil {
		return nil, err
	}
	return catalog, nil
}

type ToolSearch struct{ Catalog *tool.Catalog }

func (ToolSearch) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "tool_search", Description: "Discover and activate deferred tools available in this session.", InputSchema: schema(`{"query":{"type":"string"},"names":{"type":"array","items":{"type":"string"}}}`, "query"), Effects: []tool.Effect{tool.EffectRead}, Exposure: tool.ExposureEager}
}

func (t ToolSearch) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	var arguments struct {
		Query string   `json:"query"`
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	query := strings.ToLower(strings.TrimSpace(arguments.Query))
	requested := make(map[string]bool, len(arguments.Names))
	for _, name := range arguments.Names {
		requested[name] = true
	}
	var matches []tool.Descriptor
	for _, descriptor := range t.Catalog.Descriptors() {
		if descriptor.Name == "tool_search" || descriptor.Exposure == tool.ExposureDisabled {
			continue
		}
		haystack := strings.ToLower(descriptor.Name + " " + descriptor.Description + " " + strings.Join(descriptor.Tags, " "))
		if requested[descriptor.Name] || query == "" || strings.Contains(haystack, query) {
			matches = append(matches, descriptor)
		}
	}
	if len(matches) == 0 {
		return tool.TextResult("No matching tools."), nil
	}
	activated := make([]string, 0, len(matches))
	for _, match := range matches {
		if match.Exposure == tool.ExposureDeferred {
			if err := t.Catalog.Activate(call.Scope.SessionID, match.Name); err != nil {
				return tool.Result{}, err
			}
			activated = append(activated, match.Name)
		}
	}
	sort.Strings(activated)
	items := make([]map[string]any, 0, len(matches))
	for _, match := range matches {
		items = append(items, map[string]any{"name": match.Name, "description": match.Description, "effects": match.Effects, "activated": contains(activated, match.Name)})
	}
	data, err := json.Marshal(items)
	if err != nil {
		return tool.Result{}, fmt.Errorf("encode tool search: %w", err)
	}
	return tool.Result{Content: []model.Content{{Type: model.ContentJSON, JSON: data}}, Metadata: map[string]any{"activated": activated}}, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

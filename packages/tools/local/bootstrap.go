package localtools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/noknov/kepler-agent/packages/agent/prompt"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/mcp"
	"github.com/noknov/kepler-agent/packages/profiles/local"
	mcptools "github.com/noknov/kepler-agent/packages/tools/mcp"
	"github.com/noknov/kepler-agent/packages/tools/skills"
)

// RegisterSkillsAndMCP discovers workspace/configured skills and remote MCP
// tools and registers them into catalog. When deferred tools are present, it
// also registers tool_search so the model can discover and activate them.
func RegisterSkillsAndMCP(ctx context.Context, catalog *tool.Catalog, workspace local.Workspace, config local.Config) (string, error) {
	roots := []string{
		filepath.Join(workspace.Root, ".agents", "skills"),
		filepath.Join(workspace.Root, ".codex", "skills"),
	}
	roots = append(roots, config.SkillRoots...)
	skillCatalog, err := skills.Discover(roots)
	if err != nil {
		return "", err
	}
	if skillCatalog.Prompt() != "" {
		if err := catalog.Register(skillCatalog.Tool()); err != nil {
			return "", err
		}
	}
	for _, server := range config.MCPServers {
		if server.Name == "" || server.URL == "" {
			return "", errors.New("every mcp_servers entry requires name and url")
		}
		items, discoverErr := mcptools.Discover(ctx, mcptools.Server{
			Name: server.Name,
			Client: &mcp.Client{
				ServiceName: server.Name,
				URL:         server.URL,
				Token:       os.Getenv(server.TokenEnv),
				Headers:     server.Headers,
			},
			Effects: ParseEffects(server.Effects),
		})
		if discoverErr != nil {
			return "", fmt.Errorf("discover MCP server %s: %w", server.Name, discoverErr)
		}
		for _, item := range items {
			if registerErr := catalog.Register(item); registerErr != nil {
				return "", registerErr
			}
		}
	}
	for _, descriptor := range catalog.Descriptors() {
		if descriptor.Exposure == tool.ExposureDeferred {
			if err := catalog.Register(tool.NewSearchTool(catalog)); err != nil {
				return "", err
			}
			break
		}
	}
	return skillCatalog.Prompt(), nil
}

// BuildPrompts assembles the local prompt stack (core, product, environment,
// project instructions, user prompt overlays, and discovered file skills).
func BuildPrompts(coreID, coreContent string, config local.Config, root, skillPrompt string) ([]prompt.Fragment, error) {
	fragments := []prompt.Fragment{
		{ID: coreID, Version: "3", Layer: prompt.LayerCore, Content: coreContent},
		{ID: "local-product", Version: "5", Layer: prompt.LayerProduct, Content: "You are running locally. Filesystem writes must stay within the workspace. Exec network access requires explicit approval. Never seek or expose credentials. Do not ask the user for provider API keys."},
		{ID: "local-environment", Layer: prompt.LayerEnvironment, Content: "Workspace: " + root},
	}
	project, err := local.ProjectInstructions(root)
	if err != nil {
		return nil, err
	}
	for index, value := range project {
		fragments = append(fragments, prompt.Fragment{ID: fmt.Sprintf("project-%d", index), Layer: prompt.LayerProject, Content: value, Order: index})
	}
	overlays, err := local.LoadPromptFiles(config.PromptFiles)
	if err != nil {
		return nil, err
	}
	for index, value := range overlays {
		fragments = append(fragments, prompt.Fragment{ID: fmt.Sprintf("user-overlay-%d", index), Layer: prompt.LayerUser, Content: value, Order: index})
	}
	if skillPrompt != "" {
		fragments = append(fragments, prompt.Fragment{ID: "file-skills", Layer: prompt.LayerSkill, Content: skillPrompt})
	}
	return fragments, nil
}

// ParseEffects maps configured string effect names to typed tool.Effect values,
// defaulting to EffectExternalWrite when none are valid.
func ParseEffects(values []string) []tool.Effect {
	items := make([]tool.Effect, 0, len(values))
	for _, value := range values {
		switch tool.Effect(value) {
		case tool.EffectRead, tool.EffectWorkspaceWrite, tool.EffectExternalWrite, tool.EffectPrivileged, tool.EffectNetwork:
			items = append(items, tool.Effect(value))
		}
	}
	if len(items) == 0 {
		items = append(items, tool.EffectExternalWrite)
	}
	return items
}

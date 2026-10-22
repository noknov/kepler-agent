package localtools

import (
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
	"github.com/noknov/kepler-agent/packages/toolkit/toolprompts"
)

func NewCatalog(workspace local.Workspace, sandbox local.Sandbox) (*tool.Catalog, error) {
	catalog, err := tool.NewCatalog(
		ReadFile{Workspace: workspace}, ListFiles{Workspace: workspace}, Search{Workspace: workspace},
		WriteFile{Workspace: workspace}, EditFile{Workspace: workspace}, Exec{Sandbox: sandbox},
		MemoryTool{Workspace: workspace},
	)
	if err != nil {
		return nil, err
	}
	if err := catalog.SetPromptOverlay(toolprompts.Apply); err != nil {
		return nil, err
	}
	return catalog, nil
}

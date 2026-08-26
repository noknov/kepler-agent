package localtools

import (
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

func NewCatalog(workspace local.Workspace, sandbox local.Sandbox) (*tool.Catalog, error) {
	return tool.NewCatalog(
		ReadFile{Workspace: workspace}, ListFiles{Workspace: workspace}, Search{Workspace: workspace},
		WriteFile{Workspace: workspace}, EditFile{Workspace: workspace}, Exec{Sandbox: sandbox},
	)
}

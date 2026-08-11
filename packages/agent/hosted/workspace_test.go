package hosted

import (
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/local"
)

func testWorkspace(t *testing.T) local.Workspace {
	t.Helper()
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	return workspace
}

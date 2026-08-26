package hosted

import (
	"testing"

	"github.com/noknov/kepler-agent/packages/profiles/local"
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

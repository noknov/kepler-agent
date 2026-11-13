//go:build unix

package localtools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

func TestIntegrationExecPreservesOutputWhenCommandTimesOut(t *testing.T) {
	if os.Getenv("SANDBOX_INTEGRATION") != "1" {
		t.Skip("SANDBOX_INTEGRATION is not enabled")
	}
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	item := Exec{Sandbox: local.Sandbox{Workspace: workspace}}
	result, err := item.Execute(t.Context(), tool.Call{Arguments: json.RawMessage(`{"argv":["/bin/sh","-c","printf 'command-started\\n'; exec sleep 30"],"timeout_seconds":1}`)})
	if !errors.Is(err, context.DeadlineExceeded) || !result.IsError || !strings.Contains(result.Text(), "command-started") {
		t.Fatalf("partial command evidence lost: result=%+v err=%v", result, err)
	}
}

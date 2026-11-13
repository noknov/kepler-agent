package localtools

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

func TestExecArgv(t *testing.T) {
	got, err := execArgv([]string{"git", "status"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"git", "status"}) {
		t.Fatalf("got %v", got)
	}
	if _, err := execArgv(nil); err == nil {
		t.Fatal("expected error when argv is absent")
	}
}

func TestExecCallDescriptorUsesRequestedCapability(t *testing.T) {
	for _, test := range []struct {
		name, args       string
		network, invalid bool
	}{
		{name: "sandboxed", args: `{"argv":["rm","-rf","build"]}`},
		{name: "network", args: `{"argv":["curl","https://example.test"],"network":true}`, network: true},
		{name: "missing argv", args: `{}`, invalid: true},
		{name: "NUL argument", args: `{"argv":["printf","a\u0000b"]}`, invalid: true},
		{name: "negative timeout", args: `{"argv":["true"],"timeout_seconds":-1}`, invalid: true},
		{name: "excessive timeout", args: `{"argv":["true"],"timeout_seconds":1801}`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			descriptor, err := tool.DescribeCall(Exec{}, tool.Call{Arguments: json.RawMessage(test.args)})
			if test.invalid {
				if err == nil {
					t.Fatal("invalid call obtained a capability descriptor")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(descriptor.Effects, tool.EffectNetwork) != test.network || !slices.Contains(descriptor.Effects, tool.EffectWorkspaceWrite) {
				t.Fatalf("wrong capability contract: %+v", descriptor)
			}
		})
	}
}

func TestExecPreconditionFailureDoesNotReportSuccessfulExit(t *testing.T) {
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	args, _ := json.Marshal(map[string]any{"argv": []string{"true"}, "workdir": t.TempDir()})
	result, err := (Exec{Sandbox: local.Sandbox{Workspace: workspace}}).Execute(t.Context(), tool.Call{Arguments: args})
	if err == nil || !result.IsError {
		t.Fatalf("outside workdir accepted: %+v %v", result, err)
	}
	if _, reported := result.Metadata["exit_code"]; reported {
		t.Fatal("a command that never ran was assigned an exit code")
	}
}

package hosted

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
)

func TestPolicyAllowsReadsAndOperatorControlledWrites(t *testing.T) {
	policy := Policy{Allowed: map[string]bool{"write_file": true}}
	read, _ := policy.Decide(context.Background(), tool.PolicyRequest{Descriptor: tool.Descriptor{Name: "read_file", Effects: []tool.Effect{tool.EffectRead}}, Call: tool.Call{Name: "read_file"}})
	write, _ := policy.Decide(context.Background(), tool.PolicyRequest{Descriptor: tool.Descriptor{Name: "write_file", Effects: []tool.Effect{tool.EffectWorkspaceWrite}}, Call: tool.Call{Name: "write_file"}})
	unknownWrite, _ := policy.Decide(context.Background(), tool.PolicyRequest{Descriptor: tool.Descriptor{Name: "other", Effects: []tool.Effect{tool.EffectExternalWrite}}, Call: tool.Call{Name: "other"}})
	if read.Type != tool.DecisionAllow || write.Type != tool.DecisionAllow || unknownWrite.Type != tool.DecisionDeny {
		t.Fatalf("read=%+v write=%+v unknownWrite=%+v", read, write, unknownWrite)
	}
}

type recordingExecutor struct{ request ArgvRequest }

func (e *recordingExecutor) Execute(_ context.Context, request ArgvRequest) (ArgvResult, error) {
	e.request = request
	return ArgvResult{Output: "ok"}, nil
}

func TestExecPassesArgvWithoutShell(t *testing.T) {
	workspace := testWorkspace(t)
	executor := &recordingExecutor{}
	item := Exec{Workspace: workspace, Executor: executor, Commands: map[string]bool{"rg": true}}
	result, err := item.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{"argv":["rg","a; touch escaped"],"workdir":"."}`)})
	if err != nil || result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(executor.request.Argv) != 2 || executor.request.Argv[1] != "a; touch escaped" {
		t.Fatalf("argv=%q", executor.request.Argv)
	}
}

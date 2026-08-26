package hosted

import (
	"context"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/observability"
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

type canceledModel struct{}

func (canceledModel) Generate(context.Context, model.Request, model.EventSink) (model.Response, error) {
	return model.Response{}, context.DeadlineExceeded
}

func TestObservedBackgroundCancellationIsNotProviderError(t *testing.T) {
	metrics := observability.NewRecorder()
	client := observedModel{Client: canceledModel{}, Metrics: metrics}
	if _, err := client.Generate(context.Background(), model.Request{}, nil); err == nil {
		t.Fatal("expected model cancellation")
	}
	snapshot := metrics.Snapshot()
	if snapshot.LLMCalls != 1 || snapshot.LLMErrors != 0 {
		t.Fatalf("snapshot=%+v, want one non-error background call", snapshot)
	}
}

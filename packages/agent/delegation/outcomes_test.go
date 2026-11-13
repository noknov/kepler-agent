package delegation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type outcomeModel struct{}

func (outcomeModel) Generate(_ context.Context, req model.Request, _ model.EventSink) (model.Response, error) {
	if strings.Contains(req.Messages[len(req.Messages)-1].Text(), "limited") {
		return model.Response{Message: model.TextMessage(model.RoleAssistant, "partial findings: auth.go:42"), FinishReason: model.FinishLength}, nil
	}
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "complete findings: config.go:10"), FinishReason: model.FinishStop}, nil
}
func TestExplorePreservesIncompleteReportsAndFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     json.RawMessage
		code     string
		complete bool
	}{
		{"single", json.RawMessage(`{"task":"limited"}`), "explore_failed", false},
		{"mixed", leafBatch("limited", "successful"), "explore_partial_failure", true},
		{"all failed", leafBatch("limited one", "limited two"), "explore_failed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog, _ := tool.NewCatalog(echoReadTool{})
			store := transcript.NewMemoryStore()
			explore := ExploreTool{Runner: Runner{Config: agentruntime.Config{Model: "test"}, Deps: agentruntime.Dependencies{Model: outcomeModel{}, Transcript: store}, ParentCatalog: catalog, AllowedTools: DefaultLocalAllowedTools()}}
			result, err := explore.Execute(t.Context(), tool.Call{Scope: tool.Scope{SessionID: "parent", TurnID: "turn"}, Arguments: tc.args})
			if err != nil || !result.IsError || result.ErrorCode != tc.code {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if !strings.Contains(result.Text(), "partial findings: auth.go:42") || !strings.Contains(result.Text(), "output_limit") {
				t.Fatalf("partial report or typed failure lost: %q", result.Text())
			}
			if tc.complete && !strings.Contains(result.Text(), "complete findings: config.go:10") {
				t.Fatal("successful sibling report lost")
			}
			audits := result.Metadata["child_runs"].([]ChildRun)
			for _, audit := range audits {
				if audit.Termination == agentruntime.TerminationOutputLimit && audit.Error == "" {
					t.Fatal("incomplete child has no failure audit")
				}
			}
		})
	}
}

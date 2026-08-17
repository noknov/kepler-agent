package delegation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	agentruntime "github.com/noknov/slack-copilot-agent/packages/agent/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type echoReadTool struct{}

func (echoReadTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "search", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []tool.Effect{tool.EffectRead}, Parallel: true}
}
func (echoReadTool) Execute(_ context.Context, _ tool.Call) (tool.Result, error) {
	return tool.TextResult("found evidence"), nil
}

type scriptedExploreModel struct{ text string }

func (m scriptedExploreModel) Generate(_ context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	return model.Response{Message: model.TextMessage(model.RoleAssistant, m.text), FinishReason: model.FinishStop}, nil
}

func TestExploreToolRunsParallelJobs(t *testing.T) {
	parent, err := tool.NewCatalog(echoReadTool{})
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{
		Config:        agentruntime.Config{Model: "test"},
		Deps:          agentruntime.Dependencies{Model: scriptedExploreModel{text: "report"}, IDs: agentruntime.RandomIDs{}},
		ParentCatalog: parent,
		AllowedTools:  DefaultLocalAllowedTools(),
	}
	explore := ExploreTool{Runner: runner}
	result, err := explore.Execute(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"tasks":[{"task":"find auth"},{"task":"find billing"}]}`),
		Scope:     tool.Scope{SessionID: "ses_test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := result.Text()
	if text == "" || !strings.Contains(text, "Task 1") || !strings.Contains(text, "Task 2") || !strings.Contains(text, "report") {
		t.Fatalf("unexpected explore output: %q", text)
	}
}

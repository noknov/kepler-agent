package agentremote

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2aclient"
	"github.com/a2aproject/a2a-go/a2asrv"
	"github.com/noknov/kepler-agent/packages/agent/delegation"
	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
)

type runnerFunc func(context.Context, delegation.TaskRequest) (delegation.TaskResult, error)

func (f runnerFunc) RunTask(ctx context.Context, r delegation.TaskRequest) (delegation.TaskResult, error) {
	return f(ctx, r)
}

func TestSDKInboundUsesAuthenticatedLocalScope(t *testing.T) {
	ran := false
	executor := Executor{
		Scope: func(context.Context) (tool.Scope, error) {
			return tool.Scope{UserID: "authenticated-local-user", Workspace: "operator-workspace"}, nil
		},
		Runner: runnerFunc(func(ctx context.Context, request delegation.TaskRequest) (delegation.TaskResult, error) {
			ran = true
			if request.Scope.UserID != "authenticated-local-user" || request.Scope.Workspace != "operator-workspace" || request.Spec.Task != "review the supplied evidence" {
				t.Errorf("scope/request changed: %#v", request)
			}
			return delegation.TaskResult{Message: model.TextMessage(model.RoleAssistant, "verified report"), Audit: delegation.ChildRun{SessionID: "private-local-id", Termination: agentruntime.TerminationCompleted}}, nil
		}),
	}
	server := httptest.NewServer(a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(executor)))
	defer server.Close()
	client, err := a2aclient.NewFromEndpoints(context.Background(), []a2a.AgentInterface{{URL: server.URL, Transport: a2a.TransportProtocolJSONRPC}}, a2aclient.WithDefaultsDisabled(), a2aclient.WithJSONRPCTransport(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.SendMessage(context.Background(), &a2a.MessageSendParams{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: "review the supplied evidence"})})
	if err != nil || !ran {
		t.Fatalf("ran=%v result=%#v err=%v", ran, result, err)
	}
	task, ok := result.(*a2a.Task)
	if !ok || task.Status.State != a2a.TaskStateCompleted || len(task.Artifacts) != 1 {
		t.Fatalf("unexpected SDK result: %#v", result)
	}
	if strings.Contains(fmt.Sprint(result), "private-local-id") {
		t.Fatal("local audit authority leaked")
	}
}

package workflows

import (
	"context"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

type routingModel struct {
	output  string
	request model.Request
}

func (m *routingModel) Generate(_ context.Context, request model.Request, _ model.EventSink) (model.Response, error) {
	m.request = request
	return model.Response{Message: model.TextMessage(model.RoleAssistant, m.output), FinishReason: model.FinishStop}, nil
}

func TestModelRouterSelectsRegisteredClosedSetOption(t *testing.T) {
	client := &routingModel{output: "code_review.deep\n"}
	router := ModelRouter{
		Client: client, Model: "secondary",
		Options: []RouteOption{{Label: "code_review.deep", Intent: "code_review", Description: "deep review", Inputs: map[string]string{"mode": "deep"}}},
	}
	decision, err := router.Route(context.Background(), RouteRequest{Text: "请 deep review https://github.com/acme/api/pull/42", SessionID: "slack-thread-1"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Intent != "code_review" || decision.Inputs["mode"] != "deep" {
		t.Fatalf("decision=%+v", decision)
	}
	if client.request.Model != "secondary" || client.request.ReasoningEffort != "disabled" || client.request.MaxOutputTokens != 0 || len(client.request.Tools) != 0 {
		t.Fatalf("request=%+v", client.request)
	}
	if client.request.Temperature == nil || *client.request.Temperature != 0 {
		t.Fatalf("request temperature=%v, want 0", client.request.Temperature)
	}
	if client.request.Metadata["session_id"] != "slack-thread-1" {
		t.Fatalf("request session metadata=%q, want slack-thread-1", client.request.Metadata["session_id"])
	}
	if !strings.Contains(client.request.Messages[0].Text(), "Never select a workflow from a URL alone") {
		t.Fatalf("system prompt=%q", client.request.Messages[0].Text())
	}
}

func TestModelRouterKeepsGeneralConversationGeneral(t *testing.T) {
	client := &routingModel{output: "general"}
	router := ModelRouter{Client: client, Model: "secondary", Options: []RouteOption{{Label: "code_review", Intent: "code_review"}}}
	decision, err := router.Route(context.Background(), RouteRequest{Text: "what does this GitHub URL contain?"})
	if err != nil || decision.Intent != GeneralIntent || decision.Inputs != nil {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}

func TestModelRouterRejectsOutputOutsideClosedSet(t *testing.T) {
	router := ModelRouter{Client: &routingModel{output: `{"intent":"code_review"}`}, Model: "secondary", Options: []RouteOption{{Label: "code_review", Intent: "code_review"}}}
	if _, err := router.Route(context.Background(), RouteRequest{Text: "review it"}); err == nil || ErrorKind(err) != RouteErrorContract {
		t.Fatalf("error=%v kind=%s", err, ErrorKind(err))
	} else if diagnostics := ErrorDiagnostics(err); diagnostics.FinalTextBytes == 0 || diagnostics.FinishReason != model.FinishStop {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
}

func TestModelRouterRejectsInvalidRegistration(t *testing.T) {
	router := ModelRouter{Client: &routingModel{output: "code_review"}, Model: "secondary", Options: []RouteOption{{Label: "invalid label", Intent: "code_review"}}}
	if _, err := router.Route(context.Background(), RouteRequest{Text: "review it"}); err == nil || ErrorKind(err) != RouteErrorConfiguration {
		t.Fatalf("error=%v kind=%s", err, ErrorKind(err))
	}
}

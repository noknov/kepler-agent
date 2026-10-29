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

type routeResponseModel struct{ response model.Response }

func (m routeResponseModel) Generate(context.Context, model.Request, model.EventSink) (model.Response, error) {
	return m.response, nil
}

func TestModelRouterRequiresCompletedClassification(t *testing.T) {
	for _, label := range []string{GeneralIntent, "code_review"} {
		for _, reason := range []model.FinishReason{model.FinishLength, model.FinishContent, model.FinishCanceled, model.FinishError, model.FinishToolCalls, ""} {
			t.Run(label+"/"+string(reason), func(t *testing.T) {
				router := ModelRouter{Client: routeResponseModel{response: model.Response{Message: model.TextMessage(model.RoleAssistant, label), FinishReason: reason}}, Model: "secondary", Options: []RouteOption{{Label: "code_review", Intent: "code_review"}}}
				decision, err := router.Route(t.Context(), RouteRequest{Text: "inspect"})
				if err == nil || ErrorKind(err) != RouteErrorContract || decision.Intent != "" {
					t.Fatalf("non-completed label routed: decision=%+v err=%v", decision, err)
				}
				if diagnostics := ErrorDiagnostics(err); diagnostics.FinishReason != reason || diagnostics.FinalTextBytes != len(label) {
					t.Fatalf("classification receipt lost: %+v", diagnostics)
				}
			})
		}
	}
}

func TestModelRouterRejectsToolRequestsAlongsideLabel(t *testing.T) {
	message := model.TextMessage(model.RoleAssistant, "code_review")
	message.Content = append(message.Content, model.Content{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call", Name: "review", Arguments: []byte(`{}`)}})
	router := ModelRouter{Client: routeResponseModel{response: model.Response{Message: message, FinishReason: model.FinishStop}}, Model: "secondary", Options: []RouteOption{{Label: "code_review", Intent: "code_review"}}}
	if decision, err := router.Route(t.Context(), RouteRequest{Text: "inspect"}); err == nil || ErrorKind(err) != RouteErrorContract || decision.Intent != "" {
		t.Fatalf("classification admitted unexpected tool request: decision=%+v err=%v", decision, err)
	}
}

func (m *routingModel) Generate(_ context.Context, request model.Request, _ model.EventSink) (model.Response, error) {
	m.request = request
	return model.Response{Message: model.TextMessage(model.RoleAssistant, m.output), FinishReason: model.FinishStop}, nil
}

func TestModelRouterSelectsRegisteredClosedSetOption(t *testing.T) {
	client := &routingModel{output: "code_review\n"}
	router := ModelRouter{
		Client: client, Model: "secondary",
		Options: []RouteOption{{Label: "code_review", Intent: "code_review", Description: "pull-request review"}},
	}
	decision, err := router.Route(context.Background(), RouteRequest{Text: "请 review https://github.com/acme/api/pull/42", SessionID: "slack-thread-1"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Intent != "code_review" || len(decision.Inputs) != 0 {
		t.Fatalf("decision=%+v", decision)
	}
	if client.request.Model != "secondary" || client.request.ReasoningEffort != "disabled" || client.request.MaxOutputTokens != 512 || len(client.request.Tools) != 0 {
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

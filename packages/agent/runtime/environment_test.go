package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/environment"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestProjectionBudgetsEphemeralEnvironmentAndImages(t *testing.T) {
	input := model.TextMessage(model.RoleUser, "inspect")
	input.ID = "current"
	input.Content = append(input.Content, model.Content{Type: model.ContentImage, ImageURL: "data:image/png;base64,AAAA"})
	persisted := durableUserInput(input)
	events := []transcript.Event{{Sequence: 1, Type: transcript.UserInput, Message: &persisted}}
	p := NewBoundedProjector(ContextConfig{MaxTokens: 600})
	if _, err := p.Project(context.Background(), ProjectRequest{Events: events}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Project(context.Background(), ProjectRequest{Events: events, CurrentInput: input}); err == nil {
		t.Fatal("image cost was added after admission")
	}
	env := model.TextMessage(model.RoleUser, strings.Repeat("environment ", 600))
	if _, err := p.Project(context.Background(), ProjectRequest{Events: events, Environment: env}); err == nil {
		t.Fatal("environment cost was added after admission")
	}
	if len(events[0].Message.Content) != 2 || events[0].Message.Content[1].ImageURL != "" {
		t.Fatal("ephemeral input changed durable events")
	}
}

func TestRuntimeDoesNotDispatchOverBudgetImageRequest(t *testing.T) {
	client := &scriptedModel{}
	catalog, _ := tool.NewCatalog()
	runner, err := New(Config{Context: ContextConfig{MaxTokens: 800, ReserveTokens: 100}}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	input := model.Message{Role: model.RoleUser, Content: []model.Content{{Type: model.ContentImage, ImageURL: "data:image/png;base64,AAAA"}}}
	if _, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "s", TurnID: "t", Input: input}); err == nil {
		t.Fatal("expected input budget rejection")
	}
	if len(client.requests) != 0 {
		t.Fatal("oversized request reached provider")
	}
}

func TestProjectionKeepsEnvironmentEphemeralAndCounted(t *testing.T) {
	input := model.TextMessage(model.RoleUser, "question")
	env := environment.Config{WorkspaceRoots: []string{"/workspace"}}.Message()
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 1000}).Project(context.Background(), ProjectRequest{System: model.TextMessage(model.RoleSystem, "rules"), Environment: env, Events: []transcript.Event{{Sequence: 1, Type: transcript.UserInput, Message: &input}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Messages) != 3 || projection.Messages[1].ID != environment.MessageID || projection.Messages[2].Text() != "question" {
		t.Fatalf("messages=%+v", projection.Messages)
	}
	if projection.EstimatedTokens != EstimateTokens(projection.Messages) {
		t.Fatal("estimate differs from assembled request")
	}
}

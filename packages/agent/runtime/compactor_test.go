package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

type compactorClient struct{ request model.Request }

func (c *compactorClient) Generate(_ context.Context, request model.Request, _ model.EventSink) (model.Response, error) {
	c.request = request
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "summary")}, nil
}

func TestCompactorSplitsAnOversizedGroup(t *testing.T) {
	client := &compactorClient{}
	compactor := ModelCompactor{Client: client, MaxInputTokens: 300}
	_, err := compactor.Compact(context.Background(), []model.Message{model.TextMessage(model.RoleUser, strings.Repeat("x", 2000))}, 100)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCompactorStopsWhenSummariesDoNotShrink(t *testing.T) {
	client := &scriptedModel{responses: []model.Response{
		{Message: model.TextMessage(model.RoleAssistant, strings.Repeat("x", 1000))},
		{Message: model.TextMessage(model.RoleAssistant, strings.Repeat("x", 1000))},
		{Message: model.TextMessage(model.RoleAssistant, strings.Repeat("x", 1000))},
	}}
	compactor := ModelCompactor{Client: client, MaxInputTokens: 300}
	var input []model.Message
	for i := 0; i < 3; i++ {
		input = append(input, model.TextMessage(model.RoleUser, strings.Repeat("x", 400)))
	}
	_, err := compactor.Compact(context.Background(), input, 100)
	if err == nil || !strings.Contains(err.Error(), "did not reduce") || len(client.requests) != 3 {
		t.Fatalf("error=%v calls=%d", err, len(client.requests))
	}
}

func TestCompactionDefaultIsAnOutputCeiling(t *testing.T) {
	client := &compactorClient{}
	_, err := (ModelCompactor{Client: client}).Compact(context.Background(), []model.Message{model.TextMessage(model.RoleUser, "history")}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if client.request.MaxOutputTokens != 4096 {
		t.Fatalf("output limit=%d", client.request.MaxOutputTokens)
	}
}

func TestModelCompactorHonorsRequestedBudget(t *testing.T) {
	client := &compactorClient{}
	compactor := ModelCompactor{Client: client, Model: "summary-model", MaxOutputTokens: 4096}
	summary, err := compactor.Compact(context.Background(), []model.Message{model.TextMessage(model.RoleUser, "history")}, 512)
	if err != nil {
		t.Fatal(err)
	}
	if client.request.MaxOutputTokens != 512 {
		t.Fatalf("max output tokens = %d, want 512", client.request.MaxOutputTokens)
	}
	if summary.Role != model.RoleUser || summary.Text() != "<untrusted_transcript_summary>\nsummary\n</untrusted_transcript_summary>" {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestCompactionOmitsNestedToolImagesWithoutChangingTranscript(t *testing.T) {
	image := model.Content{Type: model.ContentImage, ImageURL: "data:image/png;base64,eA=="}
	message := model.Message{Role: model.RoleTool, Content: []model.Content{{Type: model.ContentToolResult, ToolResult: &model.ToolResult{CallID: "c", Content: []model.Content{image}}}}}
	safe := compactionSafeMessages([]model.Message{message})
	if model.ContainImages(safe...) || !model.ContainImages(message) {
		t.Fatal("nested image was retained or canonical evidence was mutated")
	}
}

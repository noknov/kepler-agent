package runtime

import (
	"context"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

type compactorClient struct{ request model.Request }

func (c *compactorClient) Generate(_ context.Context, request model.Request, _ model.EventSink) (model.Response, error) {
	c.request = request
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "summary")}, nil
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

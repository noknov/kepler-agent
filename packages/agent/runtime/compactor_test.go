package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestOversizedFragmentsPreserveUnicodeAndEverySourceByte(t *testing.T) {
	messages := []model.Message{model.TextMessage(model.RoleUser, strings.Repeat("数据库🙂; abc{}\\\"\n", 500))}
	raw, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	chunks := chunkMessages(messages, 160)
	var recovered strings.Builder
	for _, chunk := range chunks {
		if EstimateTokens(chunk) > 160 || len(chunk) != 1 || !utf8.ValidString(chunk[0].Text()) {
			t.Fatalf("invalid fragment: %+v", chunk)
		}
		_, fragment, ok := strings.Cut(chunk[0].Text(), "\n")
		if !ok {
			t.Fatal("missing fragment envelope")
		}
		recovered.WriteString(fragment)
	}
	if recovered.String() != string(raw) {
		t.Fatal("fragmentation changed source bytes")
	}
}

func TestFittingFragmentUsesLargestCompleteRuneWithinBudget(t *testing.T) {
	for _, text := range []string{"abc甲乙🙂?", strings.Repeat("数据🙂", 60)} {
		for limit := 1; limit < 100; limit++ {
			part := fittingFragment(text, "evidence: ", limit)
			if !utf8.ValidString(part) || !strings.HasPrefix(text, part) {
				t.Fatal("invalid prefix")
			}
			if part != "" && EstimateTokens([]model.Message{model.TextMessage(model.RoleUser, "evidence: "+part)}) > limit {
				t.Fatal("budget exceeded")
			}
			if len(part) < len(text) {
				_, width := utf8.DecodeRuneInString(text[len(part):])
				next := text[:len(part)+width]
				if EstimateTokens([]model.Message{model.TextMessage(model.RoleUser, "evidence: "+next)}) <= limit {
					t.Fatal("fragment is not maximal")
				}
			}
		}
	}
}

func BenchmarkOversizedUnicodeCompactionChunks(b *testing.B) {
	messages := []model.Message{model.TextMessage(model.RoleUser, strings.Repeat("数据库🙂", 6000))}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		chunkMessages(messages, 4000)
	}
}

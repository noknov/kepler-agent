package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

type nativeCompactorStub struct {
	fail     bool
	requests []model.Request
}

func (c *nativeCompactorStub) Generate(_ context.Context, r model.Request, _ model.EventSink) (model.Response, error) {
	c.requests = append(c.requests, r)
	if c.fail {
		return model.Response{}, errors.New("unsupported")
	}
	return model.Response{Message: model.Message{Role: model.RoleUser, Content: []model.Content{{Type: model.ContentContinuation, Continuation: &model.Continuation{Version: 1, Kind: "compaction", Items: []byte(`[{"type":"compaction","encrypted_content":"private"}]`), TokenEstimate: 10}}}}}, nil
}
func TestCompactorKeepsPortableCheckpointAndOmitsNativeStateFromTrace(t *testing.T) {
	native := &nativeCompactorStub{}
	compactor := ModelCompactor{Client: &compactorClient{}, NativeClient: native, Model: "m"}
	for _, failure := range []bool{false, true} {
		native.fail = failure
		checkpoint, err := compactor.Compact(context.Background(), []model.Message{model.TextMessage(model.RoleUser, "history")}, 100)
		if err != nil || !strings.Contains(checkpoint.Text(), "summary") {
			t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
		}
		if hasNativeContinuation(checkpoint) == failure {
			t.Fatal("native/fallback selection incorrect")
		}
		traced := traceMessages([]model.Message{checkpoint})
		if hasNativeContinuation(traced[0]) || traced[0].Text() != checkpoint.Text() {
			t.Fatal("opaque state leaked or portable checkpoint lost")
		}
	}
	if native.requests[0].Operation != model.OperationCompact {
		t.Fatal("native endpoint was not selected")
	}
}
func TestContextPolicyCounterAndMaskingAreConsistent(t *testing.T) {
	image := []model.Message{{Role: model.RoleUser, Content: []model.Content{{Type: model.ContentImage, ImageURL: "x"}}}}
	a := EstimatedCounter{ImageTokens: 100}
	b := EstimatedCounter{ImageTokens: 900}
	if b.Messages(image)-a.Messages(image) != 800 {
		t.Fatal("configured image accounting ignored")
	}
	invalid := ContextPolicy{Version: 99}
	if invalid.Validate() == nil {
		t.Fatal("unknown context policy accepted")
	}
	result := model.Message{Role: model.RoleTool, Content: []model.Content{{Type: model.ContentToolResult, ToolResult: &model.ToolResult{Name: "read", Content: []model.Content{{Type: model.ContentText, Text: strings.Repeat("x", 500)}}}}}}
	masked, changed := maskToolResultMessage(result, 1, ContextPolicy{MaskMinBytes: 100, PreviewBytes: 20})
	if !changed || len(masked.Content[0].ToolResult.Content[0].Text) >= 400 {
		t.Fatal("configured preview policy ignored")
	}
	if result.Content[0].ToolResult.Content[0].Text != strings.Repeat("x", 500) {
		t.Fatal("source evidence mutated")
	}
}

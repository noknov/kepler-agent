package transcript

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

func TestEventErrorClassificationSurvivesJSON(t *testing.T) {
	for _, original := range []error{
		fmt.Errorf("stopped: %w", context.Canceled),
		fmt.Errorf("timeout: %w", context.DeadlineExceeded),
		&model.Error{Kind: model.ErrorProtocol, Message: "private upstream body", Retryable: true, StatusCode: 502},
		&model.Error{Kind: model.ErrorTransient, Message: "upstream timed out", Cause: context.DeadlineExceeded},
		&model.Error{Kind: model.ErrorOutputCommitted, Message: "stream failed", Cause: &model.Error{Kind: model.ErrorProtocol, Message: "malformed output"}},
		errors.New("ordinary diagnostic"),
	} {
		t.Run(original.Error(), func(t *testing.T) {
			event := Event{Type: TurnFailed, Error: original.Error(), Failure: FailureFromError(original)}
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			var restored Event
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			got := restored.Err()
			if got == nil || got.Error() != original.Error() || model.ErrorKindOf(got) != model.ErrorKindOf(original) || errors.Is(got, context.Canceled) != errors.Is(original, context.Canceled) || errors.Is(got, context.DeadlineExceeded) != errors.Is(original, context.DeadlineExceeded) {
				t.Fatalf("original=%T %v restored=%T %v", original, original, got, got)
			}
			var wantModel, gotModel *model.Error
			if errors.As(original, &wantModel) && (!errors.As(got, &gotModel) || gotModel.Retryable != wantModel.Retryable || gotModel.StatusCode != wantModel.StatusCode) {
				t.Fatalf("model error details changed: %+v", got)
			}
		})
	}
}

func TestEventErrorUsesFactsWithoutParsingDiagnosticText(t *testing.T) {
	if err := (Event{Type: TurnCanceled}).Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled event lost its canonical state: %v", err)
	}
	if err := (Event{Type: TurnCompleted}).Err(); err != nil {
		t.Fatalf("successful event produced an error: %v", err)
	}
	if err := (Event{Type: TurnFailed, Error: "context canceled content_policy"}).Err(); errors.Is(err, context.Canceled) || model.ErrorKindOf(err) != model.ErrorUnknown {
		t.Fatalf("classified diagnostic text as structured state: %v", err)
	}
}

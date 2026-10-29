package web

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/failure"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/safety"
)

func TestFailedTurnProjectionPreservesSafeFailureCategory(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{&model.Error{Kind: model.ErrorContentPolicy, Message: "private provider body"}, failure.ContentPolicyMessage},
		{&model.Error{Kind: model.ErrorProtocol, Message: "private malformed response"}, failure.MalformedModelMessage},
		{&model.Error{Kind: model.ErrorOutputCommitted, Message: "private stream failure", Cause: &model.Error{Kind: model.ErrorProtocol, Message: "private malformed output"}}, failure.MalformedModelMessage},
		{context.DeadlineExceeded, failure.RequestTimedOutMessage},
		{errors.New("private unknown failure"), failure.ServiceUnavailableMessage},
	} {
		t.Run(test.want, func(t *testing.T) {
			event := transcript.Event{Type: transcript.TurnFailed, Error: test.err.Error(), Failure: transcript.FailureFromError(test.err)}
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			var restored transcript.Event
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			for _, source := range []transcript.Event{event, restored} {
				view, ok := ProjectEvent(source, safety.Redactor{})
				if !ok || view.Status != "failed" || view.Text != test.want {
					t.Fatalf("projection=%+v ok=%v want=%q", view, ok, test.want)
				}
			}
		})
	}
}

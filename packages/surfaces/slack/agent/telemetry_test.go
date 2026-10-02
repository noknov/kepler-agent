package slackagent

import (
	"context"
	"errors"
	"testing"
	"time"

	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestReplyTraceDistinguishesConfirmedFailedAndUncertainDelivery(t *testing.T) {
	for _, test := range []struct {
		name, messageTS, want string
		err                   error
		uncertain             bool
	}{
		{"success", "1.0", "delivered", nil, false},
		{"error", "", "failed", errors.New("private-error-body"), false},
		{"uncertain", "", "uncertain", nil, true},
		{"suppressed", "", "suppressed", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			previous := replyTracer
			replyTracer = provider.Tracer("test")
			defer func() { replyTracer = previous; _ = provider.Shutdown(context.Background()) }()
			_, reply := beginReplyTrace(context.Background(), "session", slackconversation.Request{EventID: "turn", UserID: "user"}, nil)
			stream := &slackStream{telemetry: reply, streamStartUncertain: test.uncertain}
			stream.recordFinalDelivery(test.messageTS, test.err)
			reply.end(test.err)
			attrs := map[attribute.Key]attribute.Value{}
			for _, a := range recorder.Ended()[0].Attributes() {
				attrs[a.Key] = a.Value
			}
			if attrs["langfuse.observation.metadata.delivery_status"].AsString() != test.want {
				t.Fatal(attrs)
			}
			if _, exists := attrs["langfuse.observation.metadata.delivery_success"]; exists && test.want != "delivered" && test.want != "failed" {
				t.Fatal("unconfirmed delivery was counted as success/failure")
			}
			if _, exists := attrs["langfuse.observation.metadata.user_total_ms"]; exists {
				t.Fatal("latency fabricated without a message timestamp")
			}
		})
	}
}

func TestNativeAnswerWritesSetFirstReplyAndIncludeQueueWait(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := replyTracer
	replyTracer = provider.Tracer("test")
	defer func() { replyTracer = previous; _ = provider.Shutdown(context.Background()) }()
	_, reply := beginReplyTrace(context.Background(), "session", slackconversation.Request{}, nil)
	reply.messageAt = reply.started.Add(-2 * time.Second)
	stream := newSlackStream(context.Background(), &nativeStreamingMessenger{}, slackconversation.Request{})
	stream.telemetry = reply
	if err := stream.appendNativeChunks("answer"); err != nil {
		t.Fatal(err)
	}
	ts, err := stream.Complete("answer")
	stream.recordFinalDelivery(ts, err)
	reply.end(err)
	attrs := map[attribute.Key]attribute.Value{}
	for _, a := range recorder.Ended()[0].Attributes() {
		attrs[a.Key] = a.Value
	}
	if attrs["langfuse.observation.metadata.user_first_reply_ms"].AsInt64() < 2000 || !attrs["langfuse.observation.metadata.delivery_success"].AsBool() {
		t.Fatal(attrs)
	}
}

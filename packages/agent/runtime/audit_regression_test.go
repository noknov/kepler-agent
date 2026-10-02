package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type cleanupProbeStore struct {
	transcript.Store
	target   transcript.EventType
	observed bool
}

func (s *cleanupProbeStore) Append(ctx context.Context, event transcript.Event) (transcript.Event, error) {
	if event.Type != s.target {
		return s.Store.Append(ctx, event)
	}
	s.observed = true
	if _, ok := ctx.Deadline(); !ok {
		return transcript.Event{}, errors.New("cleanup has no deadline")
	}
	<-ctx.Done()
	return transcript.Event{}, ctx.Err()
}

type auditTool struct {
	name     string
	parallel bool
	execute  func(context.Context) (tool.Result, error)
}

func (t auditTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(t.name, "fixture", tool.ObjectSchema(nil, nil), tool.WithEffects(tool.EffectRead), tool.WithParallel(t.parallel))
}
func (t auditTool) Execute(ctx context.Context, _ tool.Call) (tool.Result, error) {
	return t.execute(ctx)
}

func TestFailureAndCanceledToolPersistenceHaveDeadlines(t *testing.T) {
	for _, target := range []transcript.EventType{transcript.ModelFailed, transcript.ToolCallCompleted} {
		t.Run(string(target), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &scriptedModel{}
			var items []tool.Tool
			if target == transcript.ModelFailed {
				client.errors = []error{errors.New("provider failed")}
			} else {
				call := model.ToolCall{ID: "cancel-call", Name: "cancel-fixture", Arguments: json.RawMessage(`{}`)}
				client.responses = []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}}}}
				items = []tool.Tool{auditTool{name: call.Name, parallel: true, execute: func(context.Context) (tool.Result, error) { cancel(); return tool.TextResult("completed"), nil }}}
			}
			catalog, err := tool.NewCatalog(items...)
			if err != nil {
				t.Fatal(err)
			}
			store := &cleanupProbeStore{Store: transcript.NewMemoryStore(), target: target}
			runner, err := New(Config{CleanupTimeout: 20 * time.Millisecond}, Dependencies{Model: client, Tools: catalog, Transcript: store})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.RunTurn(ctx, TurnRequest{Input: model.TextMessage(model.RoleUser, "test")})
			if !store.observed || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("observed=%v error=%v", store.observed, err)
			}
		})
	}
}

func TestSequentialToolCannotOvertakeEarlierParallelRead(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	defer close(release)
	items := []tool.Tool{
		auditTool{name: "read", parallel: true, execute: func(context.Context) (tool.Result, error) {
			started <- "read"
			<-release
			return tool.TextResult("read"), nil
		}},
		auditTool{name: "sequential", execute: func(context.Context) (tool.Result, error) {
			started <- "sequential"
			return tool.TextResult("sequential"), nil
		}},
	}
	catalog, err := tool.NewCatalog(items...)
	if err != nil {
		t.Fatal(err)
	}
	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{
			{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "1", Name: "read", Arguments: json.RawMessage(`{}`)}},
			{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "2", Name: "sequential", Arguments: json.RawMessage(`{}`)}},
		}}},
		{Message: model.TextMessage(model.RoleAssistant, "done")},
	}}
	runner, err := New(Config{}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runner.RunTurn(ctx, TurnRequest{Input: model.TextMessage(model.RoleUser, "test")})
		done <- err
	}()
	select {
	case first := <-started:
		if first != "read" {
			t.Fatalf("first tool=%s", first)
		}
	case <-time.After(time.Second):
		t.Fatal("read never started")
	}
	select {
	case next := <-started:
		t.Fatalf("%s overlapped the preceding read", next)
	default:
	}
	// Sending once releases the reader without closing the channel used by the
	// failure cleanup above.
	release <- struct{}{}
	if next := <-started; next != "sequential" {
		t.Fatalf("next tool=%s", next)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

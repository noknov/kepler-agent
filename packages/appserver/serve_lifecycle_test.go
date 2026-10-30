package appserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type connectionProbeModel struct {
	started chan struct{}
	done    chan error
}

func (m connectionProbeModel) Generate(ctx context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	close(m.started)
	<-ctx.Done()
	m.done <- ctx.Err()
	return model.Response{}, ctx.Err()
}

type cancellationGateStore struct {
	*transcript.MemoryStore
	pending, release chan struct{}
}

func (s *cancellationGateStore) Append(ctx context.Context, event transcript.Event) (transcript.Event, error) {
	if event.Type == transcript.TurnCanceled {
		close(s.pending)
		<-s.release
	}
	return s.MemoryStore.Append(ctx, event)
}

type lifecycleBlockedWriter struct{ entered, release chan struct{} }

func (w lifecycleBlockedWriter) Write(data []byte) (int, error) {
	select {
	case w.entered <- struct{}{}:
	default:
	}
	<-w.release
	return len(data), nil
}

func TestServeConnectionEndWaitsForCanceledTurn(t *testing.T) {
	for _, ending := range []struct {
		name               string
		eof, blockedWriter bool
	}{
		{name: "EOF", eof: true}, {name: "context cancellation"}, {name: "stalled presentation", blockedWriter: true},
	} {
		t.Run(ending.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store := &cancellationGateStore{MemoryStore: transcript.NewMemoryStore(), pending: make(chan struct{}), release: make(chan struct{})}
			released := false
			t.Cleanup(func() {
				if !released {
					close(store.release)
				}
			})
			catalog, _ := tool.NewCatalog()
			probe := connectionProbeModel{started: make(chan struct{}), done: make(chan error, 1)}
			runner, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: probe, Tools: catalog, Transcript: store})
			if err != nil {
				t.Fatal(err)
			}
			input, writer := io.Pipe()
			defer writer.Close()
			defer input.Close()
			var output io.Writer = io.Discard
			stalled := lifecycleBlockedWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
			if ending.blockedWriter {
				output = stalled
				t.Cleanup(func() { close(stalled.release) })
			}
			server := New(runner, input, output)
			server.Transcript = store
			if ending.blockedWriter {
				server.notify("fixture", nil)
				<-stalled.entered
				for range cap(server.outbound) {
					server.outbound <- struct{}{}
				}
			}
			served := make(chan error, 1)
			go func() { served <- server.Serve(ctx) }()
			for _, request := range []Request{
				{Method: "initialize", Params: mustJSON(InitializeParams{ClientName: "test"})},
				{Method: "initialized"},
				{Method: "turn/start", Params: mustJSON(TurnStartParams{SessionID: "session", TurnID: "turn", Input: "hello"})},
			} {
				if _, err := fmt.Fprintln(writer, string(mustJSON(request))); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-probe.started:
			case <-time.After(time.Second):
				t.Fatal("model did not start")
			}
			if ending.eof {
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err := <-probe.done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("model error=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("accepted model request survived connection")
			}
			select {
			case <-store.pending:
			case <-time.After(time.Second):
				t.Fatal("cancellation did not reach canonical commit")
			}
			select {
			case err := <-served:
				t.Fatalf("Serve returned before cancellation commit: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			close(store.release)
			released = true
			select {
			case err := <-served:
				if (ending.eof && err != nil) || (!ending.eof && !errors.Is(err, context.Canceled)) {
					t.Fatalf("Serve error=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Serve waited on presentation after canonical cleanup")
			}
			events, err := store.Load(t.Context(), "session", 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range events {
				found = found || event.Type == transcript.TurnCanceled
			}
			if !found {
				t.Fatal("Serve returned without canonical cancellation")
			}
		})
	}
}

func TestServeCancellationUnblocksConnectionRead(t *testing.T) {
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := New(nil, input, io.Discard)
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()
	if _, err := writer.Write([]byte(" ")); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-served:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation left Serve blocked on input")
	}
}

type completedContextModel struct{ contexts chan context.Context }

func (m completedContextModel) Generate(ctx context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	m.contexts <- ctx
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop}, nil
}

func TestCompletedTurnReleasesItsContext(t *testing.T) {
	capture := completedContextModel{contexts: make(chan context.Context, 1)}
	catalog, _ := tool.NewCatalog()
	store := transcript.NewMemoryStore()
	runner, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: capture, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	var output synchronizedBuffer
	server := New(runner, strings.NewReader(""), &output)
	initializeForTest(t, server)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server.handle(ctx, Request{Method: "turn/start", Params: mustJSON(TurnStartParams{SessionID: "session", TurnID: "turn", Input: "hello"})})
	var turnCtx context.Context
	select {
	case turnCtx = <-capture.contexts:
	case <-time.After(time.Second):
		t.Fatal("model did not run")
	}
	waitForNoActiveTurns(t, server)
	select {
	case <-turnCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("completed turn retained a live context")
	}
}

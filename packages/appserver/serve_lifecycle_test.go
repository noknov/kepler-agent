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

func TestServeEOFCancelsAcceptedTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := transcript.NewMemoryStore()
	catalog, _ := tool.NewCatalog()
	probe := connectionProbeModel{started: make(chan struct{}), done: make(chan error, 1)}
	runner, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: probe, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	input, writer := io.Pipe()
	defer writer.Close()
	defer input.Close()
	var output synchronizedBuffer
	server := New(runner, input, &output)
	server.Transcript = store
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
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return at EOF")
	}
	select {
	case err := <-probe.done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("model ended with %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted model request survived its connection")
	}
	waitForNoActiveTurns(t, server)
	events, err := store.Load(t.Context(), "session", 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		found = found || event.Type == transcript.TurnCanceled
	}
	if !found {
		t.Fatal("connection cancellation was not committed to transcript")
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

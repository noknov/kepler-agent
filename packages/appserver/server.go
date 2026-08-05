// Package appserver exposes agentcore through a small JSON-RPC 2.0 protocol.
// It has no Slack dependency and can be embedded behind stdio, a socket, or a
// future network transport.
package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/noknov/slack-copilot-agent/packages/agent"
	"github.com/noknov/slack-copilot-agent/packages/agentcore"
	"github.com/noknov/slack-copilot-agent/packages/agentprotocol"
	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	"github.com/noknov/slack-copilot-agent/packages/runs"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

const JSONRPCVersion = "2.0"

type Server struct {
	Core       *agentcore.Core
	Runs       runs.Store
	Rates      observability.CostRates
	Provider   string
	Model      string
	SpillStore registry.ToolSpillStore

	writeMu  sync.Mutex
	writer   io.Writer
	sequence map[string]uint64
	runsMu   sync.Mutex
	active   map[string]*activeTurn
	threads  map[string]bool
}

type activeTurn struct {
	cancel context.CancelFunc
	steer  chan llm.Message
}

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type TurnStartParams struct {
	ThreadID      string        `json:"threadId"`
	TurnID        string        `json:"turnId,omitempty"`
	UserID        string        `json:"userId,omitempty"`
	Channel       string        `json:"channel,omitempty"`
	Model         string        `json:"model,omitempty"`
	Locale        string        `json:"locale,omitempty"`
	Question      string        `json:"question,omitempty"`
	Messages      []llm.Message `json:"messages"`
	DisabledTools []string      `json:"disabledTools,omitempty"`
}

func New(core *agentcore.Core) *Server {
	return &Server{Core: core, active: map[string]*activeTurn{}, threads: map[string]bool{}, sequence: map[string]uint64{}}
}

func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	if s == nil || s.Core == nil {
		return errors.New("app server core is required")
	}
	s.writer = out
	decoder := json.NewDecoder(bufio.NewReader(in))
	for {
		var request Request
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return fmt.Errorf("decode JSON-RPC request: %w", err)
		}
		if request.JSONRPC != "" && request.JSONRPC != JSONRPCVersion {
			s.respond(request.ID, nil, &ResponseError{Code: -32600, Message: "invalid JSON-RPC version"})
			continue
		}
		s.handle(ctx, request)
	}
}

func (s *Server) handle(ctx context.Context, request Request) {
	switch request.Method {
	case "initialize":
		s.respond(request.ID, map[string]any{
			"protocolVersion": agentprotocol.Version,
			"capabilities":    map[string]bool{"streaming": true, "cancel": true, "steering": true, "eventReplay": false},
		}, nil)
	case "turn/start":
		var params TurnStartParams
		if err := json.Unmarshal(request.Params, &params); err != nil || params.ThreadID == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "threadId and valid params are required"})
			return
		}
		if params.TurnID == "" {
			params.TurnID = agentprotocol.NewTurnID()
		}
		turnCtx, cancel := context.WithCancel(ctx)
		active := &activeTurn{cancel: cancel, steer: make(chan llm.Message, 64)}
		if !s.register(params.TurnID, active) {
			cancel()
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "turnId is already active"})
			return
		}
		s.respond(request.ID, map[string]any{"turnId": params.TurnID, "status": agentprotocol.StatusQueued}, nil)
		if s.registerThread(params.ThreadID) {
			s.Publish(ctx, agentprotocol.Event{Type: agentprotocol.ThreadStarted, ThreadID: params.ThreadID, Status: agentprotocol.StatusRunning})
		}
		go s.execute(turnCtx, params, active)
	case "turn/steer":
		var params struct {
			TurnID string `json:"turnId"`
			Text   string `json:"text"`
		}
		if json.Unmarshal(request.Params, &params) != nil || params.TurnID == "" || params.Text == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "turnId and text are required"})
			return
		}
		if !s.steer(params.TurnID, llm.Message{Role: "user", Content: params.Text}) {
			s.respond(request.ID, nil, &ResponseError{Code: -32004, Message: "active turn not found or steering queue is full"})
			return
		}
		s.respond(request.ID, map[string]bool{"queued": true}, nil)
	case "turn/cancel":
		var params struct {
			TurnID string `json:"turnId"`
		}
		if json.Unmarshal(request.Params, &params) != nil || params.TurnID == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "turnId is required"})
			return
		}
		if !s.cancel(params.TurnID) {
			s.respond(request.ID, nil, &ResponseError{Code: -32004, Message: "active turn not found"})
			return
		}
		s.respond(request.ID, map[string]bool{"canceled": true}, nil)
	case "thread/close":
		var params struct {
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(request.Params, &params) != nil || params.ThreadID == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "threadId is required"})
			return
		}
		if !s.closeThread(params.ThreadID) {
			s.respond(request.ID, nil, &ResponseError{Code: -32004, Message: "thread not found"})
			return
		}
		s.respond(request.ID, map[string]bool{"closed": true}, nil)
		s.Publish(ctx, agentprotocol.Event{Type: agentprotocol.ThreadClosed, ThreadID: params.ThreadID, Status: agentprotocol.StatusCompleted})
	default:
		s.respond(request.ID, nil, &ResponseError{Code: -32601, Message: "method not found"})
	}
}

func (s *Server) execute(ctx context.Context, params TurnStartParams, active *activeTurn) {
	defer s.unregister(params.TurnID)
	model := params.Model
	if model == "" {
		model = s.Model
	}
	var observer *runs.Observer
	if s.Runs != nil {
		observer = runs.NewObserver(s.Runs, runs.Run{
			ID: params.TurnID, SessionID: params.ThreadID, UserID: params.UserID,
			Channel: params.Channel, Provider: s.Provider, Model: model,
		}, s.Rates)
	}
	turnRequest := agentcore.TurnRequest{
		ThreadID: params.ThreadID, TurnID: params.TurnID, Model: model,
		Agent:  agentRequest(params, s.SpillStore, active.steer),
		Events: agentprotocol.SinkFunc(s.Publish),
	}
	if observer != nil {
		turnRequest.Observer = observer
	}
	result, err := s.Core.Execute(ctx, turnRequest)
	if observer != nil {
		status := "completed"
		final := result.Agent.Final
		if errors.Is(err, context.Canceled) {
			status = "interrupted"
		} else if err != nil {
			status = "error"
		} else if result.Agent.Pending {
			status = "pending_user"
			final = result.Agent.PendingQuestion
		}
		observer.Finish(status, "", err, final)
	}
}

func agentRequest(params TurnStartParams, spill registry.ToolSpillStore, steering <-chan llm.Message) agent.Request {
	return agent.Request{
		Messages: params.Messages, UserQuestion: params.Question, Locale: params.Locale,
		RunID: params.TurnID, DisabledTools: params.DisabledTools,
		Runtime: registry.Runtime{UserID: params.UserID, Channel: params.Channel, RunID: params.TurnID, ToolSpillStore: spill},
		Steering: func() []llm.Message {
			var messages []llm.Message
			for {
				select {
				case message := <-steering:
					messages = append(messages, message)
				default:
					return messages
				}
			}
		},
	}
}

func (s *Server) Publish(_ context.Context, event agentprotocol.Event) {
	event = agentprotocol.Normalize(event)
	s.writeMu.Lock()
	s.sequence[event.ThreadID]++
	event.Sequence = s.sequence[event.ThreadID]
	if s.writer != nil {
		_ = json.NewEncoder(s.writer).Encode(map[string]any{"jsonrpc": JSONRPCVersion, "method": "event", "params": event})
	}
	s.writeMu.Unlock()
}

func (s *Server) respond(id json.RawMessage, result any, responseErr *ResponseError) {
	s.write(Response{JSONRPC: JSONRPCVersion, ID: id, Result: result, Error: responseErr})
}

func (s *Server) write(value any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.writer != nil {
		_ = json.NewEncoder(s.writer).Encode(value)
	}
}

func (s *Server) register(turnID string, active *activeTurn) bool {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	if _, exists := s.active[turnID]; exists {
		return false
	}
	s.active[turnID] = active
	return true
}

func (s *Server) unregister(turnID string) {
	s.runsMu.Lock()
	delete(s.active, turnID)
	s.runsMu.Unlock()
}

func (s *Server) cancel(turnID string) bool {
	s.runsMu.Lock()
	active, ok := s.active[turnID]
	s.runsMu.Unlock()
	if ok {
		active.cancel()
	}
	return ok
}

func (s *Server) steer(turnID string, message llm.Message) bool {
	s.runsMu.Lock()
	active, ok := s.active[turnID]
	s.runsMu.Unlock()
	if !ok {
		return false
	}
	select {
	case active.steer <- message:
		return true
	default:
		return false
	}
}

func (s *Server) registerThread(threadID string) bool {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	if s.threads[threadID] {
		return false
	}
	s.threads[threadID] = true
	return true
}

func (s *Server) closeThread(threadID string) bool {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	if !s.threads[threadID] {
		return false
	}
	delete(s.threads, threadID)
	return true
}

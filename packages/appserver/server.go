// Package appserver exposes the shared agent runtime over JSON-RPC 2.0 on stdio.
package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/slack-copilot-agent/packages/agent/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
)

const JSONRPCVersion = "2.0"

type Server struct {
	Runtime    *agentruntime.Runtime
	Transcript transcript.Store
	Prompt     []prompt.Fragment
	Model      string
	Workspace  string

	reader  io.Reader
	writer  io.Writer
	writeMu sync.Mutex

	activeMu sync.Mutex
	active   map[string]*activeTurn
}

type activeTurn struct {
	cancel   context.CancelFunc
	steering *agentruntime.InputBuffer
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
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId,omitempty"`
	UserID    string `json:"userId,omitempty"`
	Model     string `json:"model,omitempty"`
	Input     string `json:"input,omitempty"`
}

func New(runtime *agentruntime.Runtime, reader io.Reader, writer io.Writer) *Server {
	return &Server{
		Runtime: runtime,
		reader:  reader,
		writer:  writer,
		active:  map[string]*activeTurn{},
	}
}

func (s *Server) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var request Request
		if err := json.Unmarshal(line, &request); err != nil {
			continue
		}
		s.handle(ctx, request)
	}
	return scanner.Err()
}

func (s *Server) handle(ctx context.Context, request Request) {
	switch request.Method {
	case "initialize":
		s.respond(request.ID, map[string]any{
			"protocol": "v2",
			"capabilities": []string{
				"thread/resume",
				"turn/start",
				"turn/steer",
				"turn/cancel",
				"event",
				"item/started",
				"item/completed",
				"turn/started",
				"turn/completed",
			},
		}, nil)
	case "thread/resume":
		var params struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || params.SessionID == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "sessionId is required"})
			return
		}
		if s.Transcript == nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32001, Message: "transcript store unavailable"})
			return
		}
		events, err := s.Transcript.Load(ctx, params.SessionID, 0)
		if err != nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32002, Message: err.Error()})
			return
		}
		s.respond(request.ID, map[string]any{"sessionId": params.SessionID, "eventCount": len(events)}, nil)
	case "turn/start":
		var params TurnStartParams
		if err := json.Unmarshal(request.Params, &params); err != nil || params.SessionID == "" || params.Input == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "sessionId and input are required"})
			return
		}
		if params.TurnID == "" {
			params.TurnID = "turn_" + params.SessionID
		}
		turnCtx, cancel := context.WithCancel(ctx)
		buffer := &agentruntime.InputBuffer{}
		if !s.register(params.TurnID, &activeTurn{cancel: cancel, steering: buffer}) {
			cancel()
			s.respond(request.ID, nil, &ResponseError{Code: -32004, Message: "turn already active"})
			return
		}
		s.respond(request.ID, map[string]string{"turnId": params.TurnID, "status": "started"}, nil)
		go s.execute(turnCtx, params, buffer)
	case "turn/steer":
		var params struct {
			TurnID string `json:"turnId"`
			Text   string `json:"text"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || params.TurnID == "" || params.Text == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "turnId and text are required"})
			return
		}
		if !s.steer(params.TurnID, params.Text) {
			s.respond(request.ID, nil, &ResponseError{Code: -32004, Message: "active turn not found"})
			return
		}
		s.respond(request.ID, map[string]bool{"queued": true}, nil)
	case "turn/cancel":
		var params struct {
			TurnID string `json:"turnId"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || params.TurnID == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "turnId is required"})
			return
		}
		if !s.cancel(params.TurnID) {
			s.respond(request.ID, nil, &ResponseError{Code: -32004, Message: "active turn not found"})
			return
		}
		s.respond(request.ID, map[string]bool{"canceled": true}, nil)
	default:
		s.respond(request.ID, nil, &ResponseError{Code: -32601, Message: "method not found"})
	}
}

func (s *Server) execute(ctx context.Context, params TurnStartParams, steering *agentruntime.InputBuffer) {
	defer s.unregister(params.TurnID)
	s.notify("turn/started", map[string]string{"turnId": params.TurnID, "sessionId": params.SessionID})
	modelName := params.Model
	if modelName == "" {
		modelName = s.Model
	}
	result, err := s.Runtime.RunTurn(ctx, agentruntime.TurnRequest{
		SessionID: params.SessionID,
		TurnID:    params.TurnID,
		Input:     model.TextMessage(model.RoleUser, params.Input),
		Prompt:    s.Prompt,
		Scope:     tool.Scope{SessionID: params.SessionID, TurnID: params.TurnID, UserID: params.UserID, Workspace: s.Workspace},
		Steering:  steering,
		Model:     modelName,
	})
	payload := map[string]any{
		"turnId":      params.TurnID,
		"termination": result.Termination,
		"message":     result.Message,
		"usage":       result.Usage,
		"steps":       result.Steps,
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	s.notify("turn/completed", payload)
}

func (s *Server) notify(method string, params any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.writer == nil {
		return
	}
	_ = json.NewEncoder(s.writer).Encode(map[string]any{"jsonrpc": JSONRPCVersion, "method": method, "params": params})
}

// NotifyEvent streams a canonical transcript event to connected clients.
func (s *Server) NotifyEvent(event transcript.Event) {
	method := "event"
	switch event.Type {
	case transcript.ToolCallStarted:
		method = "item/started"
	case transcript.ToolCallCompleted, transcript.ToolCallFailed:
		method = "item/completed"
	}
	s.notify(method, event)
}

func (s *Server) respond(id json.RawMessage, result any, responseErr *ResponseError) {
	s.write(Response{JSONRPC: JSONRPCVersion, ID: id, Result: result, Error: responseErr})
}

func (s *Server) write(value any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.writer == nil {
		return
	}
	_ = json.NewEncoder(s.writer).Encode(value)
}

func (s *Server) register(turnID string, active *activeTurn) bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if _, exists := s.active[turnID]; exists {
		return false
	}
	s.active[turnID] = active
	return true
}

func (s *Server) unregister(turnID string) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	delete(s.active, turnID)
}

func (s *Server) steer(turnID, text string) bool {
	s.activeMu.Lock()
	active := s.active[turnID]
	s.activeMu.Unlock()
	if active == nil || active.steering == nil {
		return false
	}
	return active.steering.Push(model.TextMessage(model.RoleUser, text))
}

func (s *Server) cancel(turnID string) bool {
	s.activeMu.Lock()
	active := s.active[turnID]
	s.activeMu.Unlock()
	if active == nil {
		return false
	}
	active.cancel()
	return true
}

// Package appserver exposes the shared agent runtime over JSON-RPC 2.0 on stdio.
package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

const JSONRPCVersion = "2.0"

type Server struct {
	Runtime    *agentruntime.Runtime
	Transcript transcript.Store
	Prompt     []prompt.Fragment
	Model      string
	Workspace  string
	IDs        agentruntime.IDGenerator
	Approvals  interface {
		Resolve(turnID, toolCallID, scope string) error
	}

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
		IDs:     agentruntime.RandomIDs{},
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
		s.Handle(ctx, request)
	}
	return scanner.Err()
}

// Handle processes one JSON-RPC request. Transports that do not use stdio can
// call it directly and receive the response through Server's writer.
func (s *Server) Handle(ctx context.Context, request Request) {
	switch request.Method {
	case "initialize":
		s.respond(request.ID, map[string]any{
			"protocol":     "v2",
			"capabilities": DefaultCapabilities(),
		}, nil)
	case "thread/start":
		var params ThreadStartParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "invalid params"})
			return
		}
		if params.SessionID == "" {
			params.SessionID = NewSessionID(s.IDs)
		}
		s.respond(request.ID, map[string]any{"sessionId": params.SessionID, "userId": params.UserID}, nil)
	case "thread/resume":
		var params ThreadResumeParams
		if err := json.Unmarshal(request.Params, &params); err != nil || params.SessionID == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "sessionId is required"})
			return
		}
		if s.Transcript == nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32001, Message: "transcript store unavailable"})
			return
		}
		events, err := s.Transcript.Load(ctx, params.SessionID, params.AfterSequence)
		if err != nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32002, Message: err.Error()})
			return
		}
		result := map[string]any{"sessionId": params.SessionID, "eventCount": len(events)}
		if params.IncludeEvents || params.StreamItems {
			result["items"] = itemsFromEvents(events)
		}
		s.respond(request.ID, result, nil)
		if params.StreamItems {
			for _, event := range events {
				s.NotifyEvent(event)
			}
		}
	case "thread/fork":
		var params ThreadForkParams
		if err := json.Unmarshal(request.Params, &params); err != nil || params.SourceSessionID == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "sourceSessionId is required"})
			return
		}
		if s.Transcript == nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32001, Message: "transcript store unavailable"})
			return
		}
		childID := params.ChildSessionID
		if childID == "" {
			childID = NewSessionID(s.IDs)
		}
		events, err := s.Transcript.Load(ctx, params.SourceSessionID, 0)
		if err != nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32002, Message: err.Error()})
			return
		}
		copied := 0
		for _, event := range events {
			if params.BeforeSequence > 0 && event.Sequence >= params.BeforeSequence {
				break
			}
			forked := event
			forked.SessionID = childID
			forked.ID = ""
			if _, err := s.Transcript.Append(ctx, forked); err != nil {
				s.respond(request.ID, nil, &ResponseError{Code: -32003, Message: err.Error()})
				return
			}
			copied++
		}
		s.respond(request.ID, map[string]any{"sessionId": childID, "sourceSessionId": params.SourceSessionID, "eventCount": copied}, nil)
	case "turn/start":
		var params TurnStartParams
		if err := json.Unmarshal(request.Params, &params); err != nil || params.SessionID == "" || params.Input == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "sessionId and input are required"})
			return
		}
		if params.TurnID == "" {
			params.TurnID = NewTurnID(s.IDs)
		}
		turnCtx, cancel := context.WithCancel(ctx)
		buffer := &agentruntime.InputBuffer{}
		if !s.register(params.TurnID, &activeTurn{cancel: cancel, steering: buffer}) {
			cancel()
			s.respond(request.ID, nil, &ResponseError{Code: -32004, Message: "turn already active"})
			return
		}
		s.respond(request.ID, map[string]string{"turnId": params.TurnID, "sessionId": params.SessionID, "status": "started"}, nil)
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
	case "turn/cancel", "turn/interrupt":
		var params TurnInterruptParams
		if err := json.Unmarshal(request.Params, &params); err != nil || params.TurnID == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "turnId is required"})
			return
		}
		if !s.cancel(params.TurnID) {
			s.respond(request.ID, nil, &ResponseError{Code: -32004, Message: "active turn not found"})
			return
		}
		s.respond(request.ID, map[string]bool{"canceled": true}, nil)
	case "approval/resolve":
		var params struct {
			TurnID     string `json:"turnId"`
			ToolCallID string `json:"toolCallId"`
			Scope      string `json:"scope"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || params.TurnID == "" || params.ToolCallID == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "turnId and toolCallId are required"})
			return
		}
		if s.Approvals == nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32005, Message: "approvals are unavailable"})
			return
		}
		if err := s.Approvals.Resolve(params.TurnID, params.ToolCallID, params.Scope); err != nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32006, Message: err.Error()})
			return
		}
		s.respond(request.ID, map[string]bool{"resolved": true}, nil)
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
		"sessionId":   params.SessionID,
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
	streamed := event.Type == transcript.ModelStreamed && event.Model != nil && event.Model.Type == model.StreamTextDelta
	method := NotificationMethod(string(event.Type), streamed)
	payload := any(itemFromEvent(event))
	if streamed {
		payload = map[string]any{
			"turnId":    event.TurnID,
			"sessionId": event.SessionID,
			"delta":     event.Model.Text,
		}
	}
	s.notify(method, payload)
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

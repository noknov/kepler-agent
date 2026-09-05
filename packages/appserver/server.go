// Package appserver exposes the shared agent runtime over JSON-RPC 2.0 on stdio.
package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/trajectory"
)

const JSONRPCVersion = "2.0"

type Server struct {
	Runtime    *agentruntime.Runtime
	Transcript transcript.Store
	Prompt     []prompt.Fragment
	Model      string
	Workspace  string
	// TurnTimeout bounds the complete model/tool loop. Provider HTTP deadlines
	// alone do not protect an app-server turn from a stalled tool or adapter.
	TurnTimeout time.Duration
	// MaxActiveTurns is the local process bulkhead. A client can submit work for
	// several threads, but it cannot create an unbounded number of model/tool
	// goroutines when a provider or tool becomes slow. New work is rejected with
	// a retryable JSON-RPC error while the process is saturated.
	MaxActiveTurns int
	IDs            agentruntime.IDGenerator

	reader          io.Reader
	writer          io.Writer
	outbound        chan any
	droppedOutbound atomic.Uint64
	deltas          *deltaBatcher

	activeMu sync.Mutex
	active   map[string]*activeTurn

	pendingMu sync.Mutex
	pending   map[string]*pendingApproval

	stateMu sync.Mutex
	state   connectionState
}

type activeTurn struct {
	sessionID string
	cancel    context.CancelFunc
	steering  *agentruntime.InputBuffer
	phase     turnPhase
}

type connectionState uint8

const (
	connectionUninitialized connectionState = iota
	connectionAwaitingInitialized
	connectionReady
)

type turnPhase uint8

const (
	turnRunning turnPhase = iota
	turnInterruptRequested
	turnCompleting
)

type registerResult uint8

const (
	registerOK registerResult = iota
	registerDuplicate
	registerThreadBusy
	registerOverloaded
)

type Request = ProtocolEnvelope

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

func New(runtime *agentruntime.Runtime, reader io.Reader, writer io.Writer) *Server {
	server := &Server{
		Runtime:        runtime,
		reader:         reader,
		writer:         writer,
		active:         map[string]*activeTurn{},
		IDs:            agentruntime.RandomIDs{},
		MaxActiveTurns: 8,
		outbound:       make(chan any, 1024),
	}
	server.deltas = newDeltaBatcher(defaultDeltaFlushInterval, defaultDeltaFlushBytes, server.notifyStreamDelta)
	go server.writeLoop()
	return server
}

func (s *Server) writeLoop() {
	for value := range s.outbound {
		if s.writer != nil {
			_ = json.NewEncoder(s.writer).Encode(value)
		}
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
			s.respond(json.RawMessage("null"), nil, &ResponseError{Code: -32700, Message: "parse error"})
			continue
		}
		if request.JSONRPC != "" && request.JSONRPC != JSONRPCVersion {
			s.respond(request.ID, nil, &ResponseError{Code: -32600, Message: "invalid JSON-RPC version"})
			continue
		}
		s.handle(ctx, request)
	}
	return scanner.Err()
}

func (s *Server) handle(ctx context.Context, request Request) {
	if request.Method == "initialize" {
		s.handleInitialize(request)
		return
	}
	if request.Method == "initialized" {
		if !s.acknowledgeInitialized() {
			s.respond(request.ID, nil, &ResponseError{Code: -32600, Message: "initialize must succeed before initialized"})
		}
		return
	}
	if !s.initialized() {
		s.respond(request.ID, nil, &ResponseError{Code: -32600, Message: "not initialized"})
		return
	}
	switch request.Method {
	case "thread/start":
		var params ThreadStartParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "invalid params"})
			return
		}
		if params.SessionID == "" {
			params.SessionID = NewSessionID(s.IDs)
		}
		s.respond(request.ID, ThreadStartResult{SessionID: params.SessionID, UserID: params.UserID}, nil)
	case "thread/resume":
		var params ThreadResumeParams
		if err := json.Unmarshal(request.Params, &params); err != nil || params.SessionID == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "sessionId is required"})
			return
		}
		if s.Transcript == nil {
			s.respond(request.ID, nil, &ResponseError{Code: StoreUnavailableErrorCode, Message: "transcript store unavailable"})
			return
		}
		events, err := s.Transcript.Load(ctx, params.SessionID, params.AfterSequence)
		if err != nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32002, Message: err.Error()})
			return
		}
		result := ThreadResumeResult{SessionID: params.SessionID, EventCount: len(events)}
		if params.IncludeEvents || params.StreamItems {
			result.Items = itemsFromEvents(events)
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
			s.respond(request.ID, nil, &ResponseError{Code: StoreUnavailableErrorCode, Message: "transcript store unavailable"})
			return
		}
		if s.sessionActive(params.SourceSessionID) {
			s.respond(request.ID, nil, &ResponseError{Code: ThreadBusyErrorCode, Message: "cannot fork a thread with an active turn"})
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
		forkedEvents := make([]transcript.Event, 0, len(events))
		for _, event := range events {
			if params.BeforeSequence > 0 && event.Sequence >= params.BeforeSequence {
				break
			}
			forked := event
			forked.SessionID = childID
			// The canonical store treats event IDs as global idempotency keys.
			// Forking preserves the historical payload but creates new facts in a
			// different session, so retaining the source ID would cause a
			// PostgreSQL conflict (and clearing it would collapse every copied
			// event onto the same empty primary key).
			forked.ID = s.IDs.New("evt")
			forked.Sequence = 0
			forkedEvents = append(forkedEvents, forked)
		}
		batchStore, ok := s.Transcript.(transcript.BatchStore)
		if !ok {
			s.respond(request.ID, nil, &ResponseError{Code: -32012, Message: "transcript store does not support atomic fork"})
			return
		}
		if _, err := batchStore.AppendBatch(ctx, forkedEvents); err != nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32003, Message: err.Error()})
			return
		}
		s.respond(request.ID, ThreadForkResult{SessionID: childID, SourceSessionID: params.SourceSessionID, EventCount: len(forkedEvents)}, nil)
	case "thread/trajectory":
		var params ThreadResumeParams
		if err := json.Unmarshal(request.Params, &params); err != nil || params.SessionID == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "sessionId is required"})
			return
		}
		if s.Transcript == nil {
			s.respond(request.ID, nil, &ResponseError{Code: StoreUnavailableErrorCode, Message: "transcript store unavailable"})
			return
		}
		events, err := s.Transcript.Load(ctx, params.SessionID, params.AfterSequence)
		if err != nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32002, Message: err.Error()})
			return
		}
		s.respond(request.ID, ThreadTrajectoryResult{SessionID: params.SessionID, Items: trajectory.Build(events)}, nil)
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
		switch s.register(params.TurnID, &activeTurn{sessionID: params.SessionID, cancel: cancel, steering: buffer, phase: turnRunning}) {
		case registerDuplicate:
			cancel()
			s.respond(request.ID, nil, &ResponseError{Code: -32004, Message: "turn already active"})
			return
		case registerOverloaded:
			cancel()
			s.respond(request.ID, nil, &ResponseError{Code: -32001, Message: "server overloaded; retry later"})
			return
		case registerThreadBusy:
			cancel()
			s.respond(request.ID, nil, &ResponseError{Code: ThreadBusyErrorCode, Message: "thread already has an active turn"})
			return
		}
		s.respond(request.ID, TurnStartResult{TurnID: params.TurnID, SessionID: params.SessionID, Status: "started"}, nil)
		go s.execute(turnCtx, params, buffer)
	case "turn/steer":
		var params TurnSteerParams
		if err := json.Unmarshal(request.Params, &params); err != nil || params.TurnID == "" || params.Text == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "turnId and text are required"})
			return
		}
		if !s.steer(params.TurnID, params.Text) {
			s.respond(request.ID, nil, &ResponseError{Code: -32004, Message: "active turn not found"})
			return
		}
		s.respond(request.ID, QueuedResult{Queued: true}, nil)
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
		s.respond(request.ID, TurnInterruptResult{Canceled: true}, nil)
	case "approval/respond":
		var params ApprovalRespondParams
		if err := json.Unmarshal(request.Params, &params); err != nil || params.TurnID == "" || params.ToolCallID == "" {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "turnId and toolCallId are required"})
			return
		}
		if err := s.respondApproval(params); err != nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32005, Message: err.Error()})
			return
		}
		s.respond(request.ID, ApprovalRespondResult{Scope: params.Scope}, nil)
	default:
		s.respond(request.ID, nil, &ResponseError{Code: -32601, Message: "method not found"})
	}
}

func (s *Server) handleInitialize(request Request) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.state != connectionUninitialized {
		s.respond(request.ID, nil, &ResponseError{Code: -32600, Message: "already initialized"})
		return
	}
	var params InitializeParams
	if len(request.Params) > 0 && string(request.Params) != "null" {
		if err := json.Unmarshal(request.Params, &params); err != nil {
			s.respond(request.ID, nil, &ResponseError{Code: -32602, Message: "invalid params"})
			return
		}
	}
	s.state = connectionAwaitingInitialized
	s.respond(request.ID, InitializeResult{Protocol: "v2", ProtocolVersion: ProtocolVersion, MinimumProtocolVersion: ProtocolVersion, MaximumProtocolVersion: ProtocolVersion, Capabilities: DefaultCapabilities()}, nil)
}

func (s *Server) acknowledgeInitialized() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.state != connectionAwaitingInitialized {
		return false
	}
	s.state = connectionReady
	return true
}

func (s *Server) initialized() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.state == connectionReady
}

func (s *Server) execute(ctx context.Context, params TurnStartParams, steering *agentruntime.InputBuffer) {
	defer s.unregister(params.TurnID)
	if s.TurnTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.TurnTimeout)
		defer cancel()
	}
	s.notify("turn/started", TurnStartedNotification{TurnID: params.TurnID, SessionID: params.SessionID})
	modelName := params.Model
	if modelName == "" {
		modelName = s.Model
	}
	result, err := s.Runtime.RunTurn(ctx, agentruntime.TurnRequest{
		SessionID: params.SessionID,
		TurnID:    params.TurnID,
		Input:     model.TextMessage(model.RoleUser, params.Input),
		Prompt:    s.Prompt,
		Scope:     tool.Scope{SessionID: params.SessionID, TurnID: params.TurnID, UserID: params.UserID, Workspace: s.Workspace, Values: map[string]string{"surface": "appserver"}},
		Steering:  steering,
		Model:     modelName,
	})
	payload := TurnCompletedNotification{TurnID: params.TurnID, SessionID: params.SessionID, Termination: result.Termination, Message: result.Message, Usage: result.Usage, Steps: result.Steps}
	if err != nil {
		payload.Error = err.Error()
	}
	s.deltas.flushTurn(params.TurnID)
	s.transition(params.TurnID, turnCompleting)
	// Runtime has durably finished. Do not let a stalled presentation writer
	// occupy a local admission slot; clients recover transient notices through
	// thread/resume and the canonical transcript.
	s.unregister(params.TurnID)
	s.notifyRequired("turn/completed", payload)
}

func (s *Server) notify(method string, params any) {
	s.enqueue(map[string]any{"jsonrpc": JSONRPCVersion, "method": method, "params": params}, false)
}

func (s *Server) notifyRequired(method string, params any) {
	s.enqueue(map[string]any{"jsonrpc": JSONRPCVersion, "method": method, "params": params}, true)
}

// NotifyEvent streams a canonical transcript event to connected clients.
func (s *Server) NotifyEvent(event transcript.Event) {
	streamed := event.Type == transcript.ModelStreamed && event.Model != nil && event.Model.Type == model.StreamTextDelta
	if streamed {
		s.deltas.push(event)
		return
	}
	method := NotificationMethod(string(event.Type), streamed)
	s.notify(method, itemFromEvent(event))
}

func (s *Server) notifyStreamDelta(event transcript.Event) {
	s.notify("item/agentMessage/delta", AgentMessageDeltaNotification{TurnID: event.TurnID, SessionID: event.SessionID, Delta: event.Model.Text})
}

func (s *Server) respond(id json.RawMessage, result any, responseErr *ResponseError) {
	if len(id) == 0 {
		return
	}
	s.write(Response{JSONRPC: JSONRPCVersion, ID: id, Result: result, Error: responseErr})
}

func (s *Server) write(value any) {
	s.enqueue(value, true)
}

func (s *Server) enqueue(value any, required bool) {
	if required {
		s.outbound <- value
		return
	}
	select {
	case s.outbound <- value:
	default:
		s.droppedOutbound.Add(1)
	}
}

func (s *Server) DroppedOutbound() uint64 { return s.droppedOutbound.Load() }

func (s *Server) register(turnID string, active *activeTurn) registerResult {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if _, exists := s.active[turnID]; exists {
		return registerDuplicate
	}
	for _, current := range s.active {
		if current.sessionID == active.sessionID {
			return registerThreadBusy
		}
	}
	if s.MaxActiveTurns > 0 && len(s.active) >= s.MaxActiveTurns {
		return registerOverloaded
	}
	s.active[turnID] = active
	return registerOK
}

func (s *Server) transition(turnID string, phase turnPhase) bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	active := s.active[turnID]
	if active == nil || phase < active.phase {
		return false
	}
	active.phase = phase
	return true
}

func (s *Server) sessionActive(sessionID string) bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	for _, active := range s.active {
		if active.sessionID == sessionID {
			return true
		}
	}
	return false
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
	if active.phase != turnRunning {
		return false
	}
	active.phase = turnInterruptRequested
	active.cancel()
	return true
}

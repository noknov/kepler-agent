package appserver

import (
	"encoding/json"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/trajectory"
)

const ProtocolVersion = 2

type InitializeParams struct {
	ClientName    string `json:"clientName,omitempty"`
	ClientVersion string `json:"clientVersion,omitempty"`
}

type InitializeResult struct {
	Protocol               string   `json:"protocol"`
	ProtocolVersion        int      `json:"protocolVersion"`
	MinimumProtocolVersion int      `json:"minimumProtocolVersion"`
	MaximumProtocolVersion int      `json:"maximumProtocolVersion"`
	Capabilities           []string `json:"capabilities"`
}

type ThreadStartResult struct {
	SessionID string `json:"sessionId"`
	UserID    string `json:"userId,omitempty"`
}

// ThreadStartParams creates a new conversation session.
type ThreadStartParams struct {
	SessionID string `json:"sessionId,omitempty"`
	UserID    string `json:"userId,omitempty"`
}

// ThreadResumeParams loads an existing session transcript.
type ThreadResumeParams struct {
	SessionID     string `json:"sessionId"`
	AfterSequence uint64 `json:"afterSequence,omitempty"`
	IncludeEvents bool   `json:"includeEvents,omitempty"`
	StreamItems   bool   `json:"streamItems,omitempty"`
}

type ThreadResumeResult struct {
	SessionID  string `json:"sessionId"`
	EventCount int    `json:"eventCount"`
	Items      []Item `json:"items,omitempty"`
}

// ThreadForkParams branches a session at a transcript sequence boundary.
type ThreadForkParams struct {
	SourceSessionID string `json:"sourceSessionId"`
	ChildSessionID  string `json:"childSessionId,omitempty"`
	BeforeSequence  uint64 `json:"beforeSequence,omitempty"`
}

type ThreadForkResult struct {
	SessionID       string `json:"sessionId"`
	SourceSessionID string `json:"sourceSessionId"`
	EventCount      int    `json:"eventCount"`
}

type ThreadTrajectoryResult struct {
	SessionID string            `json:"sessionId"`
	Items     []trajectory.Item `json:"items"`
}

type TurnStartParams struct {
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId,omitempty"`
	UserID    string `json:"userId,omitempty"`
	Model     string `json:"model,omitempty"`
	Input     string `json:"input"`
}

type TurnStartResult struct {
	TurnID    string `json:"turnId"`
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
}

type TurnSteerParams struct {
	TurnID string `json:"turnId"`
	Text   string `json:"text"`
}

type QueuedResult struct {
	Queued bool `json:"queued"`
}

// TurnInterruptParams cancels an active turn.
type TurnInterruptParams struct {
	TurnID string `json:"turnId"`
}

type TurnInterruptResult struct {
	Canceled bool `json:"canceled"`
}

type ApprovalRespondParams struct {
	SessionID  string `json:"sessionId"`
	TurnID     string `json:"turnId"`
	ToolCallID string `json:"toolCallId"`
	Scope      string `json:"scope"`
}

type ApprovalRespondResult struct {
	Scope string `json:"scope"`
}

type TurnStartedNotification struct {
	TurnID    string `json:"turnId"`
	SessionID string `json:"sessionId"`
}

type AgentMessageDeltaNotification struct {
	TurnID    string `json:"turnId"`
	SessionID string `json:"sessionId"`
	Delta     string `json:"delta"`
}

type TurnCompletedNotification struct {
	TurnID      string                         `json:"turnId"`
	SessionID   string                         `json:"sessionId"`
	Termination agentruntime.TerminationReason `json:"termination"`
	Message     model.Message                  `json:"message"`
	Usage       model.Usage                    `json:"usage"`
	Steps       int                            `json:"steps"`
	Error       string                         `json:"error,omitempty"`
}

// MethodSpec is the single source used by protocol generators and conformance
// tests. Runtime dispatch uses the same method names and concrete Go types.
type MethodSpec struct {
	Method string
	Params any
	Result any
}

type NotificationSpec struct {
	Method string
	Params any
}

func ProtocolMethods() []MethodSpec {
	return []MethodSpec{
		{Method: "initialize", Params: InitializeParams{}, Result: InitializeResult{}},
		{Method: "thread/start", Params: ThreadStartParams{}, Result: ThreadStartResult{}},
		{Method: "thread/resume", Params: ThreadResumeParams{}, Result: ThreadResumeResult{}},
		{Method: "thread/fork", Params: ThreadForkParams{}, Result: ThreadForkResult{}},
		{Method: "thread/trajectory", Params: ThreadResumeParams{}, Result: ThreadTrajectoryResult{}},
		{Method: "turn/start", Params: TurnStartParams{}, Result: TurnStartResult{}},
		{Method: "turn/steer", Params: TurnSteerParams{}, Result: QueuedResult{}},
		{Method: "turn/interrupt", Params: TurnInterruptParams{}, Result: TurnInterruptResult{}},
		{Method: "approval/respond", Params: ApprovalRespondParams{}, Result: ApprovalRespondResult{}},
	}
}

func ProtocolNotifications() []NotificationSpec {
	return []NotificationSpec{
		{Method: "event", Params: Item{}},
		{Method: "item/started", Params: Item{}},
		{Method: "item/updated", Params: Item{}},
		{Method: "item/completed", Params: Item{}},
		{Method: "item/agentMessage/delta", Params: AgentMessageDeltaNotification{}},
		{Method: "item/approvalRequested", Params: Item{}},
		{Method: "item/approvalResolved", Params: Item{}},
		{Method: "turn/started", Params: TurnStartedNotification{}},
		{Method: "turn/completed", Params: TurnCompletedNotification{}},
	}
}

type ProtocolEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// NotificationMethod returns the JSON-RPC notification name for a transcript event.
func NotificationMethod(eventType string, streamed bool) string {
	switch eventType {
	case "tool_call_started":
		return "item/started"
	case "tool_call_completed", "tool_call_failed":
		return "item/completed"
	case "model_streamed":
		if streamed {
			return "item/agentMessage/delta"
		}
		return "item/updated"
	case "assistant_message":
		return "item/completed"
	case "user_input", "steering_input":
		return "item/started"
	case "approval_requested":
		return "item/approvalRequested"
	case "approval_resolved":
		return "item/approvalResolved"
	default:
		return "event"
	}
}

// DefaultCapabilities lists the app-server protocol surface.
func DefaultCapabilities() []string {
	methods := ProtocolMethods()
	capabilities := make([]string, 0, len(methods)+len(ProtocolNotifications()))
	for _, method := range methods {
		if method.Method != "initialize" {
			capabilities = append(capabilities, method.Method)
		}
	}
	for _, notification := range ProtocolNotifications() {
		capabilities = append(capabilities, notification.Method)
	}
	return capabilities
}

// OverloadErrorCode is returned when the app-server bulkhead is full. Clients
// should retry the request with exponential backoff and jitter.
const OverloadErrorCode = -32001

const (
	StoreUnavailableErrorCode = -32010
	ThreadBusyErrorCode       = -32011
)

// NewTurnID allocates a unique turn identifier.
func NewTurnID(ids agentruntime.IDGenerator) string {
	if ids == nil {
		ids = agentruntime.RandomIDs{}
	}
	return ids.New("turn")
}

// NewSessionID allocates a unique session identifier.
func NewSessionID(ids agentruntime.IDGenerator) string {
	if ids == nil {
		ids = agentruntime.RandomIDs{}
	}
	return ids.New("ses")
}

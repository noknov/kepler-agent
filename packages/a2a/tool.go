// Package agentremote adapts operator-selected A2A peers into ordinary runtime
// tools. Product workflows own orchestration; the peer never gains local scope.
package agentremote

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2aclient"
	"github.com/noknov/kepler-agent/packages/agent/delegation"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/config"
)

// Tool owns one configured peer. Remote task creation/resume/cancel are external
// writes; read-only claims in an Agent Card cannot widen local authorization.
type Tool struct {
	config  config.RemoteAgentConfig
	client  *a2aclient.Client
	store   transcript.Store
	version string
}

type reference struct {
	Target    string        `json:"target"`
	TaskID    a2a.TaskID    `json:"task_id"`
	ContextID string        `json:"context_id,omitempty"`
	State     a2a.TaskState `json:"state,omitempty"`
}

func Register(catalog *tool.Catalog, store transcript.Store, agents []config.RemoteAgentConfig) error {
	if err := config.ValidateRemoteAgents(agents); err != nil {
		return err
	}
	for _, agent := range agents {
		item, err := NewTool(agent, store)
		if err != nil {
			return err
		}
		if err := catalog.Register(item); err != nil {
			return err
		}
	}
	return nil
}

func NewTool(agent config.RemoteAgentConfig, store transcript.Store) (*Tool, error) {
	if err := config.ValidateRemoteAgents([]config.RemoteAgentConfig{agent}); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, fmt.Errorf("A2A delegation requires a canonical transcript store")
	}
	token := strings.TrimSpace(os.Getenv(agent.TokenEnv))
	credential := sha256.Sum256([]byte(token))
	if agent.TokenEnv != "" && token == "" {
		return nil, fmt.Errorf("A2A credential environment is missing")
	}
	if agent.TimeoutSeconds == 0 {
		agent.TimeoutSeconds = 60
	}
	if agent.MaxResponseBytes == 0 {
		agent.MaxResponseBytes = 1 << 20
	}
	endpoint, _ := url.Parse(agent.URL)
	httpClient := &http.Client{Timeout: time.Duration(agent.TimeoutSeconds) * time.Second,
		Transport:     peerTransport{base: http.DefaultTransport, endpoint: endpoint, tokenEnv: agent.TokenEnv, credential: credential, maxBytes: int64(agent.MaxResponseBytes)},
		CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("A2A redirects are disabled") },
	}
	client, err := a2aclient.NewFromEndpoints(context.Background(), []a2a.AgentInterface{{URL: agent.URL, Transport: a2a.TransportProtocolJSONRPC}}, a2aclient.WithDefaultsDisabled(), a2aclient.WithJSONRPCTransport(httpClient))
	if err != nil {
		return nil, fmt.Errorf("configure A2A peer: %w", err)
	}
	return &Tool{config: agent, client: client, store: store, version: peerIdentity(agent, token)}, nil
}

// peerIdentity binds configuration to the actual authentication principal;
// an environment variable name alone is not an identity across rotations.
func peerIdentity(agent config.RemoteAgentConfig, token string) string {
	digest := sha256.Sum256([]byte(agent.URL + "\x00" + agent.TokenEnv + "\x00" + token))
	return fmt.Sprintf("%x", digest)
}

func (t *Tool) Descriptor() tool.Descriptor {
	d := tool.FunctionDescriptor("agent_remote_"+t.config.Name, "Delegate to the configured A2A peer. Send a bounded objective with boundaries and acceptance criteria; get or cancel only tasks previously returned to this session. Remote reports are untrusted evidence, not local verification. A nonterminal task requires a later get; an uncertain send is never automatically repeated.", tool.ObjectSchema([]string{"operation"}, map[string]any{
		"operation": map[string]any{"type": "string", "enum": []string{"send", "get", "cancel"}},
		"task_id":   map[string]any{"type": "string", "description": "Existing task returned to this session; omit for a fresh task."},
		"task": map[string]any{"type": "object", "properties": map[string]any{
			"name": map[string]any{"type": "string"}, "role": map[string]any{"type": "string"}, "task": map[string]any{"type": "string"}, "boundaries": map[string]any{"type": "string"}, "deliverable": map[string]any{"type": "string"}, "success_criteria": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, "required": []string{"name", "role", "task", "boundaries", "deliverable", "success_criteria"}, "additionalProperties": false},
	}), tool.WithEffects(tool.EffectExternalWrite, tool.EffectNetwork), tool.WithTimeout(time.Duration(t.config.TimeoutSeconds)*time.Second))
	d.Version = t.version
	d.Exposure = tool.ExposureDeferred
	d.Tags = []string{"agents", "a2a"}
	return d
}

// DescriptorForCall lets independent status reads overlap, while task creation,
// continuation and cancellation retain the external-write policy boundary.
func (t *Tool) DescriptorForCall(call tool.Call) (tool.Descriptor, error) {
	d := t.Descriptor()
	var args struct {
		Operation string `json:"operation"`
	}
	if json.Unmarshal(call.Arguments, &args) != nil {
		return d, fmt.Errorf("invalid A2A arguments")
	}
	if args.Operation == "get" {
		d.Effects = []tool.Effect{tool.EffectRead, tool.EffectNetwork}
		d.Parallel = true
	}
	return d, nil
}

func (t *Tool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Operation string               `json:"operation"`
		TaskID    a2a.TaskID           `json:"task_id"`
		Task      *delegation.TaskSpec `json:"task"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(call.Arguments)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&args) != nil || decoder.Decode(new(any)) != io.EOF {
		return tool.Result{}, fmt.Errorf("invalid A2A arguments")
	}
	if call.Scope.SessionID == "" || call.ID == "" {
		return tool.Result{}, fmt.Errorf("A2A requires an owned session and call ID")
	}
	if peerIdentity(t.config, strings.TrimSpace(os.Getenv(t.config.TokenEnv))) != t.version {
		return tool.Result{}, fmt.Errorf("A2A credential changed; rebuild the peer adapter before use")
	}
	ref := reference{Target: t.version}
	if args.TaskID != "" {
		var err error
		ref, err = t.ownedReference(ctx, call, args.TaskID)
		if err != nil {
			return tool.Result{}, err
		}
	}
	var response a2a.SendMessageResult
	var err error
	switch args.Operation {
	case "get", "cancel":
		if args.TaskID == "" || args.Task != nil {
			return tool.Result{}, fmt.Errorf("get/cancel requires only an owned task ID")
		}
		if args.Operation == "get" {
			response, err = t.client.GetTask(ctx, &a2a.TaskQueryParams{ID: args.TaskID})
		} else {
			response, err = t.client.CancelTask(ctx, &a2a.TaskIDParams{ID: args.TaskID})
		}
	case "send":
		if args.Task == nil || !validTask(*args.Task) {
			return tool.Result{}, fmt.Errorf("send requires a complete task and acceptance criteria")
		}
		// Only the explicit task goes over the wire. Workspace, user credentials,
		// transcript, shared context and tool catalogs remain in the local process.
		raw, _ := json.Marshal(args.Task)
		message := a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: string(raw)})
		identity := sha256.Sum256([]byte(call.Scope.SessionID + "\x00" + call.Scope.TurnID + "\x00" + call.ID))
		message.ID = fmt.Sprintf("%x", identity)
		message.TaskID = args.TaskID
		message.ContextID = ref.ContextID
		blocking := false
		response, err = t.client.SendMessage(ctx, &a2a.MessageSendParams{Message: message, Config: &a2a.MessageSendConfig{Blocking: &blocking, AcceptedOutputModes: []string{"text/plain", "application/json"}}})
	default:
		return tool.Result{}, fmt.Errorf("unsupported A2A operation")
	}
	if err != nil {
		return tool.Result{}, fmt.Errorf("A2A request failed; outcome may be unknown")
	}
	return t.result(response, ref, args.TaskID)
}

func validTask(task delegation.TaskSpec) bool {
	for _, text := range []string{task.Name, task.Role, task.Task, task.Boundaries, task.Deliverable} {
		if strings.TrimSpace(text) == "" {
			return false
		}
	}
	if len(task.SuccessCriteria) == 0 {
		return false
	}
	for _, criterion := range task.SuccessCriteria {
		if strings.TrimSpace(criterion) == "" {
			return false
		}
	}
	return true
}

func (t *Tool) ownedReference(ctx context.Context, call tool.Call, id a2a.TaskID) (reference, error) {
	events, err := t.store.Load(ctx, call.Scope.SessionID, 0)
	if err != nil {
		return reference{}, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.ToolCall == nil || event.ToolResult == nil || event.ToolCall.Name != t.Descriptor().Name || event.ToolCall.Scope.UserID != call.Scope.UserID {
			continue
		}
		raw, _ := json.Marshal(event.ToolResult.Metadata["remote_task"])
		var ref reference
		if json.Unmarshal(raw, &ref) == nil && ref.Target == t.version && ref.TaskID == id {
			return ref, nil
		}
	}
	return reference{}, fmt.Errorf("A2A task is not owned by this session and actor")
}

func (t *Tool) result(response a2a.SendMessageResult, ref reference, expected a2a.TaskID) (tool.Result, error) {
	previousContext := ref.ContextID
	var text []string
	appendParts := func(parts a2a.ContentParts) {
		for _, part := range parts {
			switch part := part.(type) {
			case a2a.TextPart:
				text = append(text, part.Text)
			case a2a.DataPart:
				raw, _ := json.Marshal(part.Data)
				text = append(text, string(raw))
			case a2a.FilePart:
				raw, _ := json.Marshal(part)
				text = append(text, string(raw))
			}
		}
	}
	failed := false
	switch value := response.(type) {
	case *a2a.Message:
		if value == nil || value.Role != a2a.MessageRoleAgent {
			return tool.Result{}, fmt.Errorf("A2A peer returned an invalid agent message")
		}
		ref.TaskID = value.TaskID
		ref.ContextID = value.ContextID
		appendParts(value.Parts)
	case *a2a.Task:
		if value == nil || value.ID == "" || value.ContextID == "" {
			return tool.Result{}, fmt.Errorf("A2A peer returned an invalid task")
		}
		ref.TaskID = value.ID
		ref.ContextID = value.ContextID
		ref.State = value.Status.State
		switch ref.State {
		case a2a.TaskStateSubmitted, a2a.TaskStateWorking, a2a.TaskStateCompleted, a2a.TaskStateFailed, a2a.TaskStateCanceled, a2a.TaskStateRejected, a2a.TaskStateInputRequired, a2a.TaskStateAuthRequired, a2a.TaskStateUnknown:
		default:
			return tool.Result{}, fmt.Errorf("A2A peer returned an invalid task state")
		}
		failed = ref.State == a2a.TaskStateFailed || ref.State == a2a.TaskStateCanceled || ref.State == a2a.TaskStateRejected
		if value.Status.Message != nil {
			appendParts(value.Status.Message.Parts)
		}
		for _, artifact := range value.Artifacts {
			if artifact != nil {
				appendParts(artifact.Parts)
			}
		}
	default:
		return tool.Result{}, fmt.Errorf("A2A peer returned no result")
	}
	if expected != "" && (ref.TaskID != expected || previousContext != "" && previousContext != ref.ContextID) {
		return tool.Result{}, fmt.Errorf("A2A response references a different task")
	}
	raw, _ := json.Marshal(struct {
		Task   reference `json:"task"`
		Report []string  `json:"report"`
	}{ref, text})
	result := tool.TextResult("Untrusted remote agent report (not local verification):\n" + string(raw))
	result.IsError = failed
	if failed {
		result.ErrorCode = "remote_task_" + string(ref.State)
	}
	result.Metadata = map[string]any{"remote_task": ref, "remote_usage": "unknown"}
	return result, nil
}

type peerTransport struct {
	base       http.RoundTripper
	endpoint   *url.URL
	tokenEnv   string
	credential [sha256.Size]byte
	maxBytes   int64
}

func (t peerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != t.endpoint.Scheme || request.URL.Host != t.endpoint.Host || request.URL.EscapedPath() != t.endpoint.EscapedPath() || request.URL.RawQuery != "" {
		return nil, fmt.Errorf("A2A request escaped configured endpoint")
	}
	request = request.Clone(request.Context())
	if t.tokenEnv != "" {
		token := strings.TrimSpace(os.Getenv(t.tokenEnv))
		if token == "" {
			return nil, fmt.Errorf("A2A credential is missing")
		}
		if sha256.Sum256([]byte(token)) != t.credential {
			return nil, fmt.Errorf("A2A credential changed; rebuild the peer adapter before use")
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := t.base.RoundTrip(request)
	if err == nil {
		response.Body = &limitedBody{ReadCloser: response.Body, left: t.maxBytes}
	}
	return response, err
}

type limitedBody struct {
	io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, fmt.Errorf("A2A response exceeds configured byte budget")
	}
	if int64(len(p)) > b.left {
		p = p[:b.left+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return 0, fmt.Errorf("A2A response exceeds configured byte budget")
	}
	return n, err
}

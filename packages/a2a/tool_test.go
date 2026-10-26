package agentremote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/config"
)

func TestRemoteTaskOwnershipAndExplicitLifecycle(t *testing.T) {
	t.Setenv("A2A_TEST_TOKEN", "private-credential")
	var calls atomic.Int32
	var messageID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer private-credential" {
			t.Error("missing peer authentication")
		}
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "private-local-workspace") || strings.Contains(string(raw), "local-user") {
			t.Error("local authority escaped")
		}
		var request struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Error(err)
		}
		state := "working"
		if request.Method == "message/send" {
			var params struct {
				Message struct {
					ID string `json:"messageId"`
				} `json:"message"`
				Config struct {
					Blocking bool `json:"blocking"`
				} `json:"configuration"`
			}
			if json.Unmarshal(request.Params, &params) != nil || params.Config.Blocking || params.Message.ID == "" {
				t.Error("invalid send configuration")
			}
			messageID = params.Message.ID
		} else if request.Method == "tasks/get" {
			state = "completed"
		} else if request.Method == "tasks/cancel" {
			state = "canceled"
		} else {
			t.Errorf("unexpected method: %s", request.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%q,"result":{"kind":"task","id":"remote-1","contextId":"remote-context","status":{"state":%q},"artifacts":[{"artifactId":"report","parts":[{"kind":"text","text":"peer evidence"}]}]}}`, request.ID, state)
	}))
	defer server.Close()
	store := transcript.NewMemoryStore()
	item, err := NewTool(config.RemoteAgentConfig{Name: "investigator", URL: server.URL, TokenEnv: "A2A_TEST_TOKEN"}, store)
	if err != nil {
		t.Fatal(err)
	}
	call := tool.Call{ID: "call-1", Name: item.Descriptor().Name, Scope: tool.Scope{UserID: "local-user", SessionID: "session-1", TurnID: "turn-1", Workspace: "private-local-workspace"}, Arguments: json.RawMessage(`{"operation":"send","task":{"name":"analysis","role":"investigator","task":"inspect an incident","boundaries":"read only","deliverable":"evidence","success_criteria":["identify sources"]}}`)}
	result, err := item.Execute(context.Background(), call)
	if err != nil || messageID == "" {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if _, err := store.Append(context.Background(), transcript.Event{ID: "result-1", SessionID: call.Scope.SessionID, TurnID: call.Scope.TurnID, Type: transcript.ToolCallCompleted, ToolCall: &call, ToolResult: &result}); err != nil {
		t.Fatal(err)
	}
	original := calls.Load()
	for _, scope := range []tool.Scope{{UserID: "local-user", SessionID: "other"}, {UserID: "other-user", SessionID: "session-1"}} {
		other := call
		other.Scope = scope
		other.Arguments = json.RawMessage(`{"operation":"get","task_id":"remote-1"}`)
		if _, err := item.Execute(context.Background(), other); err == nil {
			t.Fatal("foreign task ownership accepted")
		}
	}
	if calls.Load() != original {
		t.Fatal("foreign task reached peer")
	}
	for _, operation := range []string{"get", "cancel"} {
		next := call
		next.Arguments = json.RawMessage(fmt.Sprintf(`{"operation":%q,"task_id":"remote-1"}`, operation))
		result, err := item.Execute(context.Background(), next)
		if err != nil {
			t.Fatal(err)
		}
		if (operation == "cancel") != result.IsError {
			t.Fatalf("state outcome not represented: %#v", result)
		}
		descriptor, err := item.DescriptorForCall(next)
		if err != nil {
			t.Fatal(err)
		}
		if (operation == "get") != descriptor.Parallel {
			t.Fatal("read/write scheduling contract changed")
		}
	}
	// Local CLI sessions intentionally have no synthetic end-user ID. Their
	// canonical session and exact (empty) actor remain the ownership boundary.
	local := call
	local.ID = "local-call"
	local.Scope.UserID = ""
	local.Scope.SessionID = "local-session"
	localResult, err := item.Execute(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), transcript.Event{ID: "local-result", SessionID: local.Scope.SessionID, Type: transcript.ToolCallCompleted, ToolCall: &local, ToolResult: &localResult}); err != nil {
		t.Fatal(err)
	}
	local.Arguments = json.RawMessage(`{"operation":"get","task_id":"remote-1"}`)
	if _, err := item.Execute(context.Background(), local); err != nil {
		t.Fatal(err)
	}

}

func TestPeerTransportRejectsRedirectsAndOversizedResponses(t *testing.T) {
	for _, mode := range []string{"redirect", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			var escaped atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Add(1) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				} else {
					io.WriteString(w, strings.Repeat("private-sentinel", 100))
				}
			}))
			defer server.Close()
			item, err := NewTool(config.RemoteAgentConfig{Name: "peer", URL: server.URL, MaxResponseBytes: 128}, transcript.NewMemoryStore())
			if err != nil {
				t.Fatal(err)
			}
			_, err = item.Execute(context.Background(), tool.Call{ID: "c", Scope: tool.Scope{SessionID: "s", UserID: "u"}, Arguments: json.RawMessage(`{"operation":"send","task":{"name":"n","role":"r","task":"t","boundaries":"b","deliverable":"d","success_criteria":["v"]}}`)})
			if err == nil || strings.Contains(err.Error(), "private-sentinel") || escaped.Load() != 0 {
				t.Fatalf("err=%v escaped=%d", err, escaped.Load())
			}
		})
	}
}

func TestCredentialRotationCannotReuseOldTaskAuthority(t *testing.T) {
	t.Setenv("A2A_ROTATION_TOKEN", "credential-A")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%q,"result":{"kind":"task","id":"same-id","contextId":"remote-context","status":{"state":"working"}}}`, request.ID)
	}))
	defer server.Close()
	store := transcript.NewMemoryStore()
	cfg := config.RemoteAgentConfig{Name: "peer", URL: server.URL, TokenEnv: "A2A_ROTATION_TOKEN"}
	original, err := NewTool(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	call := tool.Call{ID: "send-A", Name: original.Descriptor().Name, Scope: tool.Scope{UserID: "user", SessionID: "session", TurnID: "turn"}, Arguments: json.RawMessage(`{"operation":"send","task":{"name":"analysis","role":"investigator","task":"inspect","boundaries":"read only","deliverable":"report","success_criteria":["cite evidence"]}}`)}
	result, err := original.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(context.Background(), transcript.Event{SessionID: "session", TurnID: "turn", Type: transcript.ToolCallCompleted, ToolCall: &call, ToolResult: &result}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("A2A_ROTATION_TOKEN", "credential-B")
	read := call
	read.ID = "get-after-rotation"
	read.Arguments = json.RawMessage(`{"operation":"get","task_id":"same-id"}`)
	if _, err = original.Execute(context.Background(), read); err == nil {
		t.Error("existing adapter silently switched authentication identity")
	}
	rotated, err := NewTool(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rotated.Execute(context.Background(), read); err == nil {
		t.Error("new principal inherited the previous principal's task reference")
	}
	if calls.Load() != 1 {
		t.Errorf("old authority reached peer after credential rotation: calls=%d", calls.Load())
	}
	if original.Descriptor().Version == rotated.Descriptor().Version {
		t.Error("pending approvals do not bind credential identity")
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "credential-A") || strings.Contains(string(raw), "credential-B") {
		t.Fatal("credential escaped into task evidence")
	}
}

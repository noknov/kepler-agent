package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInitializationNegotiatesBeforeNotifying(t *testing.T) {
	for _, version := range []string{"2025-03-26", "2025-06-18", "2025-11-25", "", "2099-01-01"} {
		t.Run(version, func(t *testing.T) {
			notified := false
			client := &Client{URL: "https://mcp.test", HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var request struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.Method == "notifications/initialized" {
					notified = true
					if r.Header.Get("MCP-Protocol-Version") != version {
						t.Error("negotiated version header missing")
					}
					return httpResp(202, nil, ""), nil
				}
				return httpResp(200, nil, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":%q}}`, request.ID, version)), nil
			})}}
			session, err := client.Initialize(context.Background())
			supported := version != "" && version != "2099-01-01"
			if (err == nil) != supported || notified != supported {
				t.Fatalf("session=%#v notified=%v err=%v", session, notified, err)
			}
			if supported && session.ProtocolVersion != version {
				t.Fatal("version lost for stateless session")
			}
		})
	}
}

func TestRPCResponseIdentityAndError(t *testing.T) {
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":"1","result":{}}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
		`{"id":1,"result":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-1,"message":"ambiguous"}}`,
		`{"jsonrpc":"2.0","id":1}`,
	} {
		if _, err := readRPCResponse(strings.NewReader(body), "application/json", json.RawMessage(`1`)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	body := `data: {"jsonrpc":"2.0","method":"notifications/message"}` + "\n\n" + `data: {"jsonrpc":"2.0","id":2,"result":{}}` + "\n\n" + `data: {"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"failed"}}` + "\n\n"
	_, err := readRPCResponse(strings.NewReader(body), "text/event-stream", json.RawMessage(`1`))
	var rpc RPCError
	if !errors.As(err, &rpc) || rpc.Code != -32001 {
		t.Fatalf("matched RPC error swallowed: %v", err)
	}
}

func TestSSECompletesWithoutWaitingForConnectionEOF(t *testing.T) {
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	client := &Client{URL: server.URL, HTTP: &http.Client{Timeout: 2 * time.Second}}
	result, _, err := client.rpcWithID(context.Background(), Session{}, 1, "tools/list", nil)
	if err != nil || string(result) != `{"ok":true}` {
		t.Fatalf("result=%s err=%v", result, err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("response connection was not closed")
	}
}

func TestDiscoveryRejectsCyclesDuplicateNamesAndPageExhaustion(t *testing.T) {
	for _, kind := range []string{"cycle", "duplicate", "pages"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			client := &Client{URL: "https://mcp.test", HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var req struct {
					ID json.RawMessage `json:"id"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				cursor := fmt.Sprint(calls)
				if kind == "cycle" {
					cursor = "repeat"
				}
				name := fmt.Sprint(calls)
				if kind == "duplicate" {
					name = "same"
				}
				return httpResp(200, nil, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":%q}],"nextCursor":%q}}`, req.ID, name, cursor)), nil
			})}}
			if _, err := client.ListTools(context.Background(), Session{}); err == nil {
				t.Fatal("unbounded or ambiguous discovery accepted")
			}
			if calls > maxDiscoveryPages {
				t.Fatalf("discovery escaped limit: %d", calls)
			}
		})
	}
}

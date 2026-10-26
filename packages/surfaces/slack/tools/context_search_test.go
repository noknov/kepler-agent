package slacktool

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

type recordingSearchSource struct {
	client *slack.Client
	err    error
	calls  []tool.Call
}

func (s *recordingSearchSource) Client(_ context.Context, call tool.Call) (*slack.Client, error) {
	s.calls = append(s.calls, call)
	return s.client, s.err
}

func TestUserSearchPreservesRequestIdentityAndStructuredEvidence(t *testing.T) {
	calls := 0
	source := &recordingSearchSource{client: slack.NewTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResp(`{"ok":true,"results":{"messages":[{"channel_id":"C1","message_ts":"1.0","content":"Decision","permalink":"https://example.slack.com/archives/C1/p1000000"}]},"response_metadata":{"next_cursor":"more"}}`), nil
	}))}
	call := tool.Call{ID: "search", Arguments: json.RawMessage(`{"query":"What did we decide?"}`), Scope: tool.Scope{UserID: "U1", SessionID: "s", TurnID: "t"}}
	result, err := (UserSearchTool{Source: source}).Execute(context.Background(), call)
	if err != nil || calls != 1 || len(source.calls) != 1 || source.calls[0].Scope.UserID != "U1" || len(result.Content) != 1 || result.Content[0].Type != model.ContentJSON {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
	var evidence slack.ContextSearchResult
	if err := json.Unmarshal(result.Content[0].JSON, &evidence); err != nil || len(evidence.Messages) != 1 || evidence.Messages[0].Permalink == "" || evidence.NextCursor != "more" {
		t.Fatalf("lost structured source/cursor: %+v err=%v", evidence, err)
	}
}

func TestUserSearchValidatesBeforeCredentialLookup(t *testing.T) {
	source := &recordingSearchSource{}
	for _, input := range []string{`{}`, `{"query":"x","limit":21}`, `{"query":"x","channel_types":["all"]}`} {
		if _, err := (UserSearchTool{Source: source}).Execute(context.Background(), tool.Call{Arguments: json.RawMessage(input)}); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	if len(source.calls) != 0 {
		t.Fatal("invalid input resolved credentials")
	}
}

func TestUserSearchCatalogHasNoBotFallback(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertToken(context.Background(), "U-other", connections.ProviderSlack, "other-user-token", nil, "U-other"); err != nil {
		t.Fatal(err)
	}
	service := &connections.Service{Store: store, Config: connections.Config{PublicBaseURL: "https://example.com", SecretKey: strings.Repeat("k", 32), Slack: connections.SlackOAuthConfig{ClientID: "id", ClientSecret: "secret"}}}
	for _, connected := range []bool{false, true} {
		catalog, _ := tool.NewCatalog()
		var conn *connections.Service
		if connected {
			conn = service
		}
		bot := slack.NewTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("workspace search used bot credentials")
			return nil, nil
		}))
		if err := AddToCatalog(catalog, tool.SurfacePolicy{Surface: "slack"}, config.Config{}, bot, conn); err != nil {
			t.Fatal(err)
		}
		item, ok := catalog.Get("slack-user_search")
		if ok != connected {
			t.Fatalf("search exposure connected=%t registered=%t", connected, ok)
		}
		if !ok {
			continue
		}
		result, err := item.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{"query":"question"}`), Scope: tool.Scope{UserID: "U-unconnected"}})
		if err != nil || result.ErrorCode != "connection_required" {
			t.Fatalf("no scoped connection request: %+v err=%v", result, err)
		}
	}
}

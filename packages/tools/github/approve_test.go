package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

func TestApprovePRPinsHeadAndSubmitsOnlyApproval(t *testing.T) {
	head := strings.Repeat("a", 40)
	for _, scenario := range []string{"success", "head-changed", "draft", "closed", "self", "uncertain", "bad-receipt", "forbidden", "bad-json"} {
		t.Run(scenario, func(t *testing.T) {
			posts := 0
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost {
					posts++
					if r.URL.Path != "/repos/example/repo/pulls/1/reviews" {
						t.Fatalf("unexpected write %s", r.URL.Path)
					}
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if len(body) != 2 || body["event"] != "APPROVE" || body["commit_id"] != head {
						t.Fatalf("write body=%v", body)
					}
					switch scenario {
					case "uncertain":
						return nil, errors.New("response lost")
					case "bad-receipt":
						return response(200, `{"id":1,"state":"PENDING"}`), nil
					case "forbidden":
						return response(403, `{"private":"do not echo"}`), nil
					case "bad-json":
						return response(200, `{"id":1} trailing`), nil
					}
					return response(200, `{"id":1,"state":"APPROVED","commit_id":"`+head+`","html_url":"https://github.com/example/repo/pull/1#pullrequestreview-1","user":{"login":"owner"}}`), nil
				}
				if r.URL.Path == "/user" {
					return response(200, `{"login":"owner"}`), nil
				}
				author := "author"
				if scenario == "self" {
					author = "owner"
				}
				current := head
				if scenario == "head-changed" {
					current = strings.Repeat("b", 40)
				}
				draft := scenario == "draft"
				state := "open"
				if scenario == "closed" {
					state = "closed"
				}
				data, _ := json.Marshal(map[string]any{"state": state, "draft": draft, "head": map[string]string{"sha": current}, "user": map[string]string{"login": author}})
				return response(200, string(data)), nil
			})
			item := ApprovePRTool{Client: testClient("example", "repo", transport)}
			args, _ := json.Marshal(map[string]string{"url": "https://github.com/example/repo/pull/1", "head_sha": head})
			result, err := item.Execute(context.Background(), tool.Call{Arguments: args})
			switch scenario {
			case "success", "draft":
				if err != nil || result.IsError || posts != 1 {
					t.Fatalf("result=%+v err=%v posts=%d", result, err, posts)
				}
			case "uncertain", "bad-receipt", "bad-json":
				if err != nil || result.ErrorCode != "external_outcome_unknown" || posts != 1 {
					t.Fatalf("result=%+v err=%v posts=%d", result, err, posts)
				}
			case "forbidden":
				if err == nil || strings.Contains(err.Error(), "do not echo") || posts != 1 {
					t.Fatalf("err=%v posts=%d", err, posts)
				}
			default:
				if err == nil || posts != 0 {
					t.Fatalf("err=%v posts=%d", err, posts)
				}
			}
		})
	}
}

func TestApprovalContractBindsCredential(t *testing.T) {
	a := ApprovePRTool{Client: Client{Token: "one"}}.Descriptor()
	b := ApprovePRTool{Client: Client{Token: "two"}}.Descriptor()
	if a.Version == b.Version || a.Version == "" {
		t.Fatal("credential rotation did not invalidate approval")
	}
}

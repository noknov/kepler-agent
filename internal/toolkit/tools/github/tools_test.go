package github

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

func TestDispatchWorkflowTool(t *testing.T) {
	var gotBody struct {
		Ref    string            `json:"ref"`
		Inputs map[string]string `json:"inputs"`
	}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path != "/repos/example/deployments/actions/workflows/deploy.yml/dispatches" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ghp-test" {
			t.Fatalf("Authorization = %q", got)
		}
		return response(http.StatusNoContent, ""), nil
	})

	tool := DispatchWorkflowTool{Client: Client{
		Token:      "ghp-test",
		APIBaseURL: "https://api.github.test",
		Owner:      "example",
		Repo:       "deployments",
		HTTP:       &http.Client{Transport: transport},
	}}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{
		"workflow":"deploy.yml",
		"ref":"main",
		"inputs":{"branch":"mt-main","deploy":true,"count":2}
	}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotBody.Ref != "main" || gotBody.Inputs["branch"] != "mt-main" || gotBody.Inputs["deploy"] != "true" || gotBody.Inputs["count"] != "2" {
		t.Fatalf("body = %#v", gotBody)
	}
	if !strings.Contains(result.Content, "deploy.yml") {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestWorkflowRunsTool(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/repos/example/deployments/actions/workflows/all.yml/runs" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("branch") != "main" || r.URL.Query().Get("per_page") != "2" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		return response(http.StatusOK, `{"workflow_runs":[{"id":123,"name":"deploy","head_branch":"main","head_sha":"abcdef123","status":"completed","conclusion":"success","event":"workflow_dispatch","html_url":"https://github.test/run","created_at":"now"}]}`), nil
	})

	tool := WorkflowRunsTool{Client: Client{
		Token:      "ghp-test",
		APIBaseURL: "https://api.github.test",
		Owner:      "example",
		Repo:       "deployments",
		HTTP:       &http.Client{Transport: transport},
	}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"workflow":"all.yml","branch":"main","limit":2}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "#123 deploy") || !strings.Contains(result.Content, "conclusion=success") {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestResolveWorkflow(t *testing.T) {
	if got := resolveWorkflow("custom.yml"); got != "custom.yml" {
		t.Fatalf("resolveWorkflow(custom.yml) = %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     http.Header{},
	}
}

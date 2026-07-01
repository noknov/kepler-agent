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
		"inputs":{"branch":"main","deploy":true,"count":2}
	}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotBody.Ref != "main" || gotBody.Inputs["branch"] != "main" || gotBody.Inputs["deploy"] != "true" || gotBody.Inputs["count"] != "2" {
		t.Fatalf("body = %#v", gotBody)
	}
	if !strings.Contains(result.Content, "deploy.yml") {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestDispatchWorkflowExecutesDirectly(t *testing.T) {
	called := false
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return response(http.StatusNoContent, ""), nil
	})
	tool := DispatchWorkflowTool{Client: Client{
		Token:      "ghp-test",
		APIBaseURL: "https://api.github.test",
		Owner:      "example",
		Repo:       "deployments",
		HTTP:       &http.Client{Transport: transport},
	}}
	raw := json.RawMessage(`{"workflow":"deploy.yml","ref":"main"}`)

	result, err := tool.Execute(context.Background(), raw, registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !called || !strings.Contains(result.Content, "deploy.yml") {
		t.Fatalf("expected dispatch to execute directly, called=%v result=%#v", called, result)
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

func TestJobLogsTool_ListsAndFetchesFailedJobs(t *testing.T) {
	calls := map[string]int{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path := r.URL.Path
		calls[path]++
		if strings.HasSuffix(path, "/jobs") {
			return response(http.StatusOK, `{"jobs":[
				{"id":100,"name":"build","status":"completed","conclusion":"success","steps":[]},
				{"id":200,"name":"test","status":"completed","conclusion":"failure","steps":[
					{"name":"Run tests","status":"completed","conclusion":"failure","number":3}
				]}
			]}`), nil
		}
		if strings.Contains(path, "/jobs/200/logs") {
			return response(http.StatusOK, "FAIL TestSomething\nExpected 1, got 2\n"), nil
		}
		t.Fatalf("unexpected path: %s", path)
		return nil, nil
	})

	tool := JobLogsTool{Client: Client{
		Token:      "ghp-test",
		APIBaseURL: "https://api.github.test",
		Owner:      "example",
		Repo:       "myrepo",
		HTTP:       &http.Client{Transport: transport},
	}}
	result, err := tool.Execute(context.Background(),
		json.RawMessage(`{"run_id":999}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "1 failed") {
		t.Fatalf("expected failure count, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "FAIL TestSomething") {
		t.Fatalf("expected log content, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "test (id=200)") {
		t.Fatalf("expected job name in output, got: %s", result.Content)
	}
	if calls["/repos/example/myrepo/actions/jobs/100/logs"] != 0 {
		t.Fatal("should not fetch logs for successful job")
	}
}

func TestJobLogsTool_DirectJobID(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "/jobs/42/logs") {
			t.Fatalf("expected direct job log fetch, got: %s", r.URL.Path)
		}
		return response(http.StatusOK, "error: build failed\n"), nil
	})

	tool := JobLogsTool{Client: Client{
		Token:      "ghp-test",
		APIBaseURL: "https://api.github.test",
		Owner:      "example",
		Repo:       "myrepo",
		HTTP:       &http.Client{Transport: transport},
	}}
	result, err := tool.Execute(context.Background(),
		json.RawMessage(`{"run_id":999,"job_id":42}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "build failed") {
		t.Fatalf("expected log content, got: %s", result.Content)
	}
}

func TestJobLogsTool_StringRunID(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/jobs") {
			return response(http.StatusOK, `{"jobs":[
				{"id":300,"name":"test","status":"completed","conclusion":"failure","steps":[]}
			]}`), nil
		}
		if strings.Contains(r.URL.Path, "/jobs/300/logs") {
			return response(http.StatusOK, "FAIL test\n"), nil
		}
		t.Fatalf("unexpected path: %s", r.URL.Path)
		return nil, nil
	})

	tool := JobLogsTool{Client: Client{
		Token:      "ghp-test",
		APIBaseURL: "https://api.github.test",
		Owner:      "example",
		Repo:       "myrepo",
		HTTP:       &http.Client{Transport: transport},
	}}
	// LLMs often pass numbers as strings
	result, err := tool.Execute(context.Background(),
		json.RawMessage(`{"run_id":"28497898370"}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "FAIL test") {
		t.Fatalf("expected log content, got: %s", result.Content)
	}
}

func TestExtractFailureSection_Short(t *testing.T) {
	log := "line1\nline2\nFAIL something\nline4\n"
	result := extractFailureSection(log)
	if result != log {
		t.Fatalf("short logs should be returned as-is, got: %s", result)
	}
}

func TestExtractFailureSection_Long(t *testing.T) {
	var lines []string
	for i := 0; i < 1000; i++ {
		lines = append(lines, "ok line")
	}
	lines[500] = "FAIL TestFoo: expected 1 got 2"
	log := strings.Join(lines, "\n")
	result := extractFailureSection(log)
	if !strings.Contains(result, "FAIL TestFoo") {
		t.Fatal("should contain failure line")
	}
	if len(strings.Split(result, "\n")) >= 900 {
		t.Fatalf("should have trimmed output, got %d lines", len(strings.Split(result, "\n")))
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

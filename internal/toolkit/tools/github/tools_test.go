package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

	tool := DispatchWorkflowTool{Client: testClient("example", "deployments", transport)}

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

func TestPRDiffStoresReviewContextAndIndexesFiles(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Header.Get("Accept") {
		case "application/vnd.github.diff":
			return response(http.StatusOK, "diff --git a/src/a.ts b/src/a.ts\n@@ -1 +1 @@\n-old\n+new\ndiff --git a/src/b.ts b/src/b.ts\n@@ -4,0 +5 @@\n+added\n"), nil
		default:
			return response(http.StatusOK, `{"title":"Review me","state":"open","commits":1,"head":{"ref":"feature/pr","sha":"0123456789abcdef"},"base":{"ref":"main"}}`), nil
		}
	})
	rt := registry.Runtime{Cache: registry.NewRuntimeCache()}
	tool := PRDiffTool{Client: testClient("example", "repo", transport)}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"pr":42}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "-old\n+new") || !strings.Contains(result.Content, "src/a.ts (hunks=1 +1/-1)") {
		t.Fatalf("expected compact diff manifest, got %q", result.Content)
	}
	pr, ok := prDiffContextFromRuntime(rt)
	if !ok || pr.Repository != "example/repo" || pr.HeadSHA != "0123456789abcdef" || len(pr.ChangedFiles) != 2 {
		t.Fatalf("PR context = %#v, ok=%v", pr, ok)
	}
	fileResult, err := (PRFileDiffTool{}).Execute(context.Background(), json.RawMessage(`{"path":"src/a.ts"}`), rt)
	if err != nil || !strings.Contains(fileResult.Content, "-old\n+new") || strings.Contains(fileResult.Content, "src/b.ts") {
		t.Fatalf("file diff = %#v, err=%v", fileResult, err)
	}
}

func TestDispatchWorkflowExecutesDirectly(t *testing.T) {
	called := false
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return response(http.StatusNoContent, ""), nil
	})
	tool := DispatchWorkflowTool{Client: testClient("example", "deployments", transport)}
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

	tool := WorkflowRunsTool{Client: testClient("example", "deployments", transport)}
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

	tool := JobLogsTool{Client: testClient("example", "myrepo", transport)}

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

	tool := JobLogsTool{Client: testClient("example", "myrepo", transport)}

	result, err := tool.Execute(context.Background(),
		json.RawMessage(`{"run_id":999,"job_id":42}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "build failed") {
		t.Fatalf("expected log content, got: %s", result.Content)
	}
}

func TestJobLogsTool_ExtractsDirectJobFromURL(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/repos/ClareAI/whatsapp_inbox/actions/jobs/84468047388/logs" {
			t.Fatalf("expected direct job log fetch from URL, got: %s", r.URL.Path)
		}
		return response(http.StatusOK, "Expected outcome.IsSuccess to be true, but found False.\n"), nil
	})

	tool := JobLogsTool{Client: testClient("", "", transport)}
	result, err := tool.Execute(context.Background(),
		json.RawMessage(`{"url":"https://github.com/ClareAI/whatsapp_inbox/actions/runs/28497898370/job/84468047388?pr=15027"}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "outcome.IsSuccess") {
		t.Fatalf("expected direct job log content, got: %s", result.Content)
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

	tool := JobLogsTool{Client: testClient("example", "myrepo", transport)}

	result, err := tool.Execute(context.Background(),
		json.RawMessage(`{"run_id":"28497898370"}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "FAIL test") {
		t.Fatalf("expected log content, got: %s", result.Content)
	}
}

func TestJobLogsTool_PaginatesRunJobs(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/jobs") {
			if strings.Contains(r.URL.Path, "/jobs/150/logs") {
				return response(http.StatusOK, "page two failure log\n"), nil
			}
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			var jobs []string
			for i := 1; i <= 100; i++ {
				jobs = append(jobs, fmt.Sprintf(`{"id":%d,"name":"job-%d","status":"completed","conclusion":"success"}`, i, i))
			}
			return response(http.StatusOK, `{"jobs":[`+strings.Join(jobs, ",")+`]}`), nil
		case "2":
			return response(http.StatusOK, `{"jobs":[{"id":150,"name":"late-test","status":"completed","conclusion":"failure"}]}`), nil
		default:
			t.Fatalf("unexpected page: %s", r.URL.RawQuery)
			return nil, nil
		}
	})

	tool := JobLogsTool{Client: testClient("example", "myrepo", transport)}
	result, err := tool.Execute(context.Background(),
		json.RawMessage(`{"run_id":999}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "late-test") || !strings.Contains(result.Content, "page two failure log") {
		t.Fatalf("expected failed job from page two, got: %s", result.Content)
	}
}

func TestPaginateLog_DefaultTail(t *testing.T) {
	var lines []string
	for i := 1; i <= 500; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	content := strings.Join(lines, "\n")
	result := paginateLog(content, 0, 200)

	if !strings.Contains(result, "lines 301-500 of 500 total") {
		t.Fatalf("expected tail position header, got: %s", result[:80])
	}
	if !strings.Contains(result, "line 500") {
		t.Fatal("should contain the last line")
	}
	if strings.Contains(result, "line 1\n") {
		t.Fatal("should not contain the first line")
	}
}

func TestPaginateLog_StartLine(t *testing.T) {
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	content := strings.Join(lines, "\n")
	result := paginateLog(content, 10, 20)

	if !strings.Contains(result, "lines 10-29 of 100 total") {
		t.Fatalf("expected correct position header, got: %s", result[:60])
	}
	if !strings.Contains(result, "line 10") || !strings.Contains(result, "line 29") {
		t.Fatal("should contain requested range")
	}
}

func TestPaginateLog_NegativeStartLine(t *testing.T) {
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	content := strings.Join(lines, "\n")
	result := paginateLog(content, -50, 30)

	if !strings.Contains(result, "lines 51-80 of 100 total") {
		t.Fatalf("expected correct position header, got: %s", result[:60])
	}
}

func TestPaginateLog_ShortLog(t *testing.T) {
	content := "line 1\nline 2\nline 3"
	result := paginateLog(content, 0, 200)

	if !strings.Contains(result, "lines 1-3 of 3 total") {
		t.Fatalf("short log should return all lines, got: %s", result)
	}
	if strings.Contains(result, "start_line=1") {
		t.Fatal("should not suggest start_line when showing from beginning")
	}
}

func TestJobLogsTool_Pagination(t *testing.T) {
	var lines []string
	for i := 1; i <= 300; i++ {
		lines = append(lines, fmt.Sprintf("log line %d", i))
	}
	logContent := strings.Join(lines, "\n")

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/jobs/42/logs") {
			return response(http.StatusOK, logContent), nil
		}
		t.Fatalf("unexpected path: %s", r.URL.Path)
		return nil, nil
	})

	tool := JobLogsTool{Client: testClient("example", "myrepo", transport)}

	// First call: default tail
	result, err := tool.Execute(context.Background(),
		json.RawMessage(`{"run_id":999,"job_id":42}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "lines 101-300 of 300 total") {
		t.Fatalf("expected tail pagination, got: %s", result.Content[:80])
	}

	// Second call: request beginning
	result, err = tool.Execute(context.Background(),
		json.RawMessage(`{"run_id":999,"job_id":42,"start_line":1,"max_lines":100}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "lines 1-100 of 300 total") {
		t.Fatalf("expected head pagination, got: %s", result.Content[:80])
	}
	if !strings.Contains(result.Content, "log line 1") {
		t.Fatal("should contain first line")
	}
}

func TestResolveWorkflow(t *testing.T) {
	if got := resolveWorkflow("custom.yml"); got != "custom.yml" {
		t.Fatalf("resolveWorkflow(custom.yml) = %q", got)
	}
}

// test helpers

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     http.Header{},
	}
}

func testClient(owner, repo string, transport roundTripFunc) Client {
	return Client{
		Token:      "ghp-test",
		APIBaseURL: "https://api.github.test",
		Owner:      owner,
		Repo:       repo,
		HTTP:       &http.Client{Transport: transport},
	}
}

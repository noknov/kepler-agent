package benchkit

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSuiteGradesContains(t *testing.T) {
	suite := Suite{
		Name: "smoke",
		Cases: []Case{{
			ID:       "c1",
			Category: "read-code",
			Prompt:   "mention gateway",
			Graders:  []Grader{{Type: "contains", Value: "gateway"}},
		}},
	}
	var out bytes.Buffer
	summary, results, err := RunSuite(context.Background(), suite, EchoAgent{}, &out, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Passed != 1 || len(results) != 1 || !results[0].Passed {
		t.Fatalf("unexpected result summary=%#v results=%#v", summary, results)
	}
	if !strings.Contains(out.String(), `"id":"c1"`) {
		t.Fatalf("jsonl output missing case: %s", out.String())
	}
}

func TestSuiteValidateRejectsDuplicateIDs(t *testing.T) {
	suite := Suite{
		Name: "bad",
		Cases: []Case{
			{ID: "same", Category: "debug", Prompt: "a"},
			{ID: "same", Category: "debug", Prompt: "b"},
		},
	}
	if err := suite.Validate(); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestRunSuiteUsesIsolatedWorkspaceCopy(t *testing.T) {
	template := t.TempDir()
	if err := os.WriteFile(filepath.Join(template, "answer.txt"), []byte("template"), 0o600); err != nil {
		t.Fatal(err)
	}
	evalRoot := t.TempDir()
	suite := Suite{
		Name: "workspace",
		Cases: []Case{{
			ID:        "copy",
			Category:  "complete-work",
			Prompt:    "mutate",
			Workspace: template,
			Graders:   []Grader{{Type: "file_contains", Path: "answer.txt", Value: "changed"}},
		}},
	}
	agent := CommandAgent{Command: []string{"sh", "-c", "printf changed > answer.txt"}}

	summary, results, err := RunSuite(context.Background(), suite, agent, nil, RunOptions{WorkspaceRoot: evalRoot, KeepWorkspaces: true})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Passed != 1 || len(results) != 1 || !results[0].Passed {
		t.Fatalf("summary=%#v results=%#v", summary, results)
	}
	original, err := os.ReadFile(filepath.Join(template, "answer.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != "template" {
		t.Fatalf("template was mutated: %q", original)
	}
	copied, err := os.ReadFile(filepath.Join(evalRoot, "copy", "answer.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(copied) != "changed" {
		t.Fatalf("copy content = %q", copied)
	}
}

func TestRunSuiteCapturesPatch(t *testing.T) {
	template := t.TempDir()
	if err := os.WriteFile(filepath.Join(template, "answer.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evalRoot := t.TempDir()
	suite := Suite{
		Name: "patch",
		Cases: []Case{{
			ID:        "diff",
			Category:  "complete-work",
			Prompt:    "mutate",
			Workspace: template,
			Graders:   []Grader{{Type: "patch_contains", Value: "after"}},
		}},
	}
	agent := CommandAgent{Command: []string{"sh", "-c", "printf 'after\n' > answer.txt"}}

	_, results, err := RunSuite(context.Background(), suite, agent, nil, RunOptions{WorkspaceRoot: evalRoot, KeepWorkspaces: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Passed {
		t.Fatalf("results=%#v", results)
	}
	if !strings.Contains(results[0].Patch, "+after") {
		t.Fatalf("patch = %q", results[0].Patch)
	}
}

func TestLoadSuiteResolvesWorkspaceRelativeToSuiteFile(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "fixtures", "case")
	if err := os.MkdirAll(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	suitePath := filepath.Join(root, "suites", "suite.json")
	if err := os.MkdirAll(filepath.Dir(suitePath), 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"name":"relative","cases":[{"id":"c1","category":"debug","prompt":"p","workspace":"../fixtures/case"}]}`
	if err := os.WriteFile(suitePath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	suite, err := LoadSuite(suitePath)
	if err != nil {
		t.Fatal(err)
	}
	if suite.Cases[0].Workspace != fixture {
		t.Fatalf("workspace = %q, want %q", suite.Cases[0].Workspace, fixture)
	}
}

func TestLoadHumanEvalSuiteRunsGeneratedCase(t *testing.T) {
	root := t.TempDir()
	problemPath := filepath.Join(root, "HumanEval.jsonl")
	problem := `{"task_id":"HumanEval/0","prompt":"def add_one(x):\n    \"\"\"Return x plus one.\"\"\"\n","canonical_solution":"    return x + 1\n","test":"def check(candidate):\n    assert candidate(1) == 2\n    assert candidate(-1) == 0\n","entry_point":"add_one"}`
	if err := os.WriteFile(problemPath, []byte(problem+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	suite, err := LoadHumanEvalSuite(problemPath, HumanEvalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Cases) != 1 {
		t.Fatalf("cases = %d", len(suite.Cases))
	}
	agent := CommandAgent{Command: []string{"sh", "-c", "python3 - <<'PY'\nfrom pathlib import Path\np=Path('solution.py')\ns=p.read_text()\np.write_text(s.replace('    pass', '    return x + 1'))\nPY"}}
	_, results, err := RunSuite(context.Background(), suite, agent, nil, RunOptions{WorkspaceRoot: filepath.Join(root, "eval"), KeepWorkspaces: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Passed {
		t.Fatalf("results=%#v", results)
	}
}

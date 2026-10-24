package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDependencyDirections(t *testing.T) {
	for _, test := range []struct {
		source, target string
		denied         bool
	}{
		{"packages/agent/runtime/a.go", module + "packages/agent/model", false},
		{"packages/agent/runtime/a.go", module + "packages/agent/modelx", true},
		{"packages/agent/model/a.go", module + "packages/agent/runtime", true},
		{"packages/agent/transcript/a.go", module + "packages/agent/tool", false},
		{"packages/agent/tool/a.go", module + "packages/agent/runtime", true},
		{"packages/agent/runtime/a.go", module + "packages/connections", true},
		{"packages/agent/new_domain/a.go", "context", true},
		{"packages/agent/runtime/a.go", "github.com/slack-go/slack", true},
		{"packages/agent/runtime/a.go", "go.opentelemetry.io/otel/trace", false},
		{"packages/agent/runtime/a.go", "go.opentelemetry.io/otelx", true},
		{"packages/providers/a.go", module + "packages/llm/chatgpt", false},
		{"packages/providers/a.go", module + "packages/llmx", true},
		{"packages/profiles/local/a.go", module + "packages/surfaces/web", true},
		{"packages/profiles/local/a.go", module + "packages/providers", false},
		{"packages/tools/foo/a.go", module + "packages/surfaces/slack/client", true},
		{"packages/connections/a.go", module + "worker", true},
		{"packages/connections/a.go", strings.TrimSuffix(module, "/"), true},
		{"packages/connections/a_test.go", module + "worker", true},
		{"evals/a.go", module + "packages/agent/runtime", true},
	} {
		t.Run(test.source+"->"+test.target, func(t *testing.T) {
			got := importViolation(test.source, test.target)
			if (got != "") != test.denied {
				t.Fatalf("reason=%q denied=%v", got, test.denied)
			}
		})
	}
}

func TestParseImportsRatherThanSourceText(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.go")
	source := `package fixture
import forbidden /* reason */ "github.com/noknov/kepler-agent/packages/surfaces/slack"
// import "github.com/noknov/kepler-agent/packages/surfaces/slack"
const documentation = "import github.com/noknov/kepler-agent/worker"
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	failures, err := checkFile("packages/agent/runtime/a.go", path)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || !strings.Contains(failures[0], "[architecture]") {
		t.Fatalf("failures=%v", failures)
	}
}

func TestRuntimeDDLLiteralsAndFixtureExemption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	source := `package fixture
const migration = "/* leading comment */ ALTER TABLE x ADD y TEXT"
func apply(db DB,ctx Context) {
 db.Exec(ctx,migration)
 db.Query(ctx,"SELECT ';DROP TABLE x'")
 db.Exec(ctx,"CREATE "+"TABLE x(id TEXT)")
}
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	failures, err := checkFile("packages/storage/a.go", path)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 2 {
		t.Fatalf("failures=%v", failures)
	}
	failures, err = checkFile("packages/storage/a_test.go", path)
	if err != nil || len(failures) != 0 {
		t.Fatalf("test fixture failures=%v err=%v", failures, err)
	}
}

func TestEveryPlatformIsCheckedAndCachesAreExcluded(t *testing.T) {
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "-b", "main", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".cache/\nevals/results/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"packages/agent/runtime/a_windows.go", ".cache/broken.go", "evals/results/generated.go"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		content := "invalid source"
		if strings.HasSuffix(path, "a_windows.go") {
			content = "//go:build windows\n\npackage fixture\nimport _ \"" + module + "worker\"\n"
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	failures, err := checkRepository(root)
	if err != nil || len(failures) != 1 {
		t.Fatalf("failures=%v err=%v", failures, err)
	}
}

func TestDDLScanner(t *testing.T) {
	for _, test := range []struct {
		sql    string
		denied bool
	}{
		{"SELECT 1", false},
		{"SELECT $$; DROP TABLE x$$", false},
		{"SELECT $body$; DROP TABLE x$body$; CREATE TABLE y(id int)", true}, {"SELECT 'DROP TABLE x; CREATE TABLE x'", false},
		{"SELECT 1; /* reason */ DROP TABLE x", true}, {"-- migration\nCREATE TABLE x(id int)", true},
		{"/* outer /* inner */ end */ ALTER TABLE x ADD y TEXT", true},
		{"INSERT INTO t(v) VALUES('a;''DROP TABLE t')", false},
		{"DO $$ BEGIN EXECUTE 'CREATE TABLE x(id int)'; END $$", true},
		{"TRUNCATE t", true}, {"GRANT SELECT ON t TO x", true},
	} {
		if got := ddlStatement(test.sql); got != test.denied {
			t.Errorf("%q: denied=%v want=%v", test.sql, got, test.denied)
		}
	}
}

func TestEmptyUnknownCoreDomainIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(path, []byte("package fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	failures, err := checkFile("packages/agent/accidental/a.go", path)
	if err != nil || len(failures) != 1 {
		t.Fatalf("failures=%v err=%v", failures, err)
	}
}

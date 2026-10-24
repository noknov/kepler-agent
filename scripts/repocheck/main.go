// repocheck enforces repository structure from parsed Go, including files for
// other platforms. It deliberately makes no network calls or code changes.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const module = "github.com/noknov/kepler-agent/"

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	failures, err := checkRepository(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, failure := range failures {
		fmt.Fprintln(os.Stderr, failure)
	}
	if len(failures) > 0 {
		os.Exit(1)
	}
	fmt.Println("Repository Go architecture contracts passed")
}

func within(path, root string) bool { return path == root || strings.HasPrefix(path, root+"/") }

// These edges describe the core's actual responsibilities, not its current
// transitive import closure. New core domains require a deliberate rule change.
var coreDependencies = map[string][]string{
	"model": {}, "prompt": {}, "taskstate": {},
	"environment": {"model"}, "failure": {"model"},
	"tool": {"model", "taskstate"}, "transcript": {"model", "tool"},
	"runtime":    {"model", "prompt", "environment", "tool", "transcript", "taskstate"},
	"delegation": {"model", "prompt", "runtime", "tool", "transcript"},
}

func importViolation(source, target string) string {
	local := strings.TrimPrefix(target, module)
	internal := local != target
	if target == strings.TrimSuffix(module, "/") {
		local = ""
		internal = true
	}
	sourceDir := filepath.ToSlash(filepath.Dir(source))
	if within(sourceDir, "packages/agent") {
		domain := strings.Split(strings.TrimPrefix(sourceDir, "packages/agent/"), "/")[0]
		allowed, known := coreDependencies[domain]
		if !known {
			return "unknown core domain: define its contract and dependency direction before adding it"
		}
		if internal {
			if within(local, "packages/agent/"+domain) {
				return ""
			}
			for _, dependency := range allowed {
				if within(local, "packages/agent/"+dependency) {
					return ""
				}
			}
			return "core dependency direction violated: move product/storage/wire behavior to an adapter, or pass a canonical interface"
		}
		first := strings.Split(target, "/")[0]
		if strings.Contains(first, ".") && !within(target, "go.opentelemetry.io/otel") {
			return "provider/transport dependencies belong in adapters; core only admits its explicit telemetry dependency"
		}
	}
	if !internal {
		return ""
	}
	if within(sourceDir, "packages") && !within(local, "packages") {
		return "libraries cannot depend on executable composition roots; inject the dependency through a package contract"
	}
	if within(sourceDir, "packages/providers") && !within(local, "packages/agent/model") && !within(local, "packages/llm") && !within(local, "packages/providers") {
		return "providers translate model contracts; product composition belongs in profiles"
	}
	if within(sourceDir, "packages/profiles") && (within(local, "packages/llm") || within(local, "packages/surfaces")) {
		return "profiles compose providers and shared contracts; surfaces depend on profiles, not the reverse"
	}
	if within(sourceDir, "packages/tools") && within(local, "packages/surfaces") {
		return "generic tools cannot depend on a surface; use a surface adapter or inject a neutral interface"
	}
	if within(local, "packages/tools/registry") {
		return "retired registry: use packages/agent/tool"
	}
	if within(sourceDir, "evals") {
		return "evaluators must exercise the public executable/API boundary"
	}
	return ""
}

func checkRepository(root string) ([]string, error) {
	// Source ownership follows Git, including staged/new work, rather than an
	// expanding list of cache/vendor/evaluator-output directory exemptions.
	command := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "*.go")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("enumerate repository Go sources: %w", err)
	}
	var failures []string
	seen := map[string]bool{}
	for _, relative := range strings.Split(string(output), "\x00") {
		if relative == "" || seen[relative] {
			continue
		}
		seen[relative] = true
		path := filepath.Join(root, relative)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		found, err := checkFile(filepath.ToSlash(relative), path)
		if err != nil {
			return nil, err
		}
		failures = append(failures, found...)
	}
	return failures, nil
}

func checkFile(relative, path string) ([]string, error) {
	positions := token.NewFileSet()
	file, err := parser.ParseFile(positions, path, nil, 0)
	if err != nil {
		return nil, err
	}
	var failures []string
	if within(filepath.ToSlash(filepath.Dir(relative)), "packages/agent") {
		domain := strings.Split(strings.TrimPrefix(filepath.ToSlash(filepath.Dir(relative)), "packages/agent/"), "/")[0]
		if _, known := coreDependencies[domain]; !known {
			return []string{relative + ":1 [architecture] unknown core domain: define its contract and dependency direction before adding it"}, nil
		}
	}
	add := func(pos token.Pos, rule, reason string) {
		failures = append(failures, fmt.Sprintf("%s:%d [%s] %s", relative, positions.Position(pos).Line, rule, reason))
	}
	for _, item := range file.Imports {
		target, err := strconv.Unquote(item.Path.Value)
		if err != nil {
			return nil, err
		}
		if reason := importViolation(relative, target); reason != "" {
			add(item.Pos(), "architecture", target+": "+reason)
		}
	}
	// Integration tests may intentionally create fixtures. Runtime SQL literals
	// are inspected at execution calls; this is not a complete SQL/type analyzer.
	if strings.HasSuffix(relative, "_test.go") {
		return failures, nil
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch method.Sel.Name {
		case "Exec", "Query", "QueryRow", "ExecContext", "QueryContext", "QueryRowContext", "Prepare", "PrepareContext":
		default:
			return true
		}
		for _, argument := range call.Args {
			if sql, ok := literalString(argument, map[*ast.Object]bool{}); ok && ddlStatement(sql) {
				add(argument.Pos(), "runtime-ddl", "move schema changes to schema/postgres.sql and an incremental kepler-agent-deploy migration")
			}
		}
		return true
	})
	return failures, nil
}

func literalString(expression ast.Expr, seen map[*ast.Object]bool) (string, bool) {
	switch value := expression.(type) {
	case *ast.BasicLit:
		if value.Kind == token.STRING {
			text, err := strconv.Unquote(value.Value)
			return text, err == nil
		}
	case *ast.BinaryExpr:
		if value.Op == token.ADD {
			a, ok := literalString(value.X, seen)
			b, other := literalString(value.Y, seen)
			return a + b, ok && other
		}
	case *ast.ParenExpr:
		return literalString(value.X, seen)
	case *ast.Ident:
		if value.Obj == nil || value.Obj.Kind != ast.Con || seen[value.Obj] {
			return "", false
		}
		seen[value.Obj] = true
		defer delete(seen, value.Obj)
		declaration, ok := value.Obj.Decl.(*ast.ValueSpec)
		if ok {
			for i, name := range declaration.Names {
				if name.Name == value.Name && i < len(declaration.Values) {
					return literalString(declaration.Values[i], seen)
				}
			}
		}
	}
	return "", false
}

// Scan statement-leading words, skipping quoted SQL and comments. DDL inside
// DO blocks is rejected by rejecting runtime DO itself, not parsing PL/pgSQL.
func ddlStatement(sql string) bool {
	start := true
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			end := strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				return false
			}
			i += end + 1
		case strings.HasPrefix(sql[i:], "/*"):
			depth := 1
			i += 2
			for i < len(sql) && depth > 0 {
				if strings.HasPrefix(sql[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(sql[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
		case sql[i] == '\'' || sql[i] == '"':
			quote := sql[i]
			i++
			start = false
			for i < len(sql) {
				if sql[i] == quote {
					i++
					if i < len(sql) && sql[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
		case sql[i] == '$':
			end := i + 1
			for end < len(sql) && ((sql[end] >= 'a' && sql[end] <= 'z') || (sql[end] >= 'A' && sql[end] <= 'Z') || (sql[end] >= '0' && sql[end] <= '9') || sql[end] == '_') {
				end++
			}
			if end < len(sql) && sql[end] == '$' {
				delimiter := sql[i : end+1]
				if close := strings.Index(sql[end+1:], delimiter); close >= 0 {
					i = end + 1 + close + len(delimiter)
					start = false
					continue
				}
			}
			i++
		case sql[i] == ';':
			start = true
			i++
		case (sql[i] >= 'a' && sql[i] <= 'z') || (sql[i] >= 'A' && sql[i] <= 'Z'):
			end := i + 1
			for end < len(sql) && ((sql[end] >= 'a' && sql[end] <= 'z') || (sql[end] >= 'A' && sql[end] <= 'Z') || sql[end] == '_') {
				end++
			}
			if start {
				switch strings.ToUpper(sql[i:end]) {
				case "CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE", "COMMENT", "DO":
					return true
				}
			}
			start = false
			i = end
		default:
			i++
		}
	}
	return false
}

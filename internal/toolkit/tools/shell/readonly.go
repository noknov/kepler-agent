package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

const maxOutputBytes = 60000

// ReadOnlyTool executes shell commands (including pipelines) for operational
// investigation. It allows a broad set of read-oriented tools — git, kubectl,
// gcloud, gh, grep, jq, and standard Unix utilities — and blocks commands that
// could modify production systems or cluster state.
//
// Supports pipes (|) so that idiomatic shell one-liners like
// "kubectl get pods | grep web" or "git log --oneline | head -20" work as-is.
// Shell meta-operators (;, &&, ||, &) and redirections (>, >>) are not allowed.
//
// The shell process runs with its working directory set to the first
// WorkspaceRoot (if any). Absolute path arguments that fall outside all roots
// are rejected to prevent agents from reading code in unrelated repositories.
type ReadOnlyTool struct {
	GCloudPath     string
	KubectlPath    string
	WorkspaceRoots []string
	Guard          safety.CommandPolicy
	Timeout        time.Duration
}

func (t ReadOnlyTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"shell",
		"Run a shell command and return its stdout. "+
			"Supports pipelines with | so you can compose commands like "+
			"\"kubectl get pods -n mt-prod | grep web\" or "+
			"\"git -C /path/to/repo log --oneline | head -20\". "+
			"Allowed commands: git (all read subcommands: log, blame, diff, grep, show, fetch, ls-files, etc.), "+
			"kubectl (get, describe, logs, top, config), gcloud (list/describe), "+
			"gh (run/pr/issue list and view), "+
			"grep, rg, find, jq, awk, sed, cat, head, tail, wc, sort, uniq, tr, cut, date, echo, curl. "+
			"Write operations (git push/commit, kubectl delete/apply, gcloud mutations, rm) are blocked. "+
			"Shell operators && ; & > >> are not supported; use | only.",
		registry.ObjectSchema([]string{"command"}, map[string]any{
			"command": map[string]any{
				"type": "string",
				"description": "The shell command to run, written exactly as you would type it in a terminal. " +
					"Examples: " +
					"\"git -C /Users/shelton/Documents/wati/wati-frontend-app log -S 'QuickReply' origin/channel-x/deploy --oneline\", " +
					"\"kubectl get pods -n mt-prod | grep instagram\", " +
					"\"grep -r 'MessageType' /Users/shelton/Documents/wati/whatsapp_inbox/netcore-mvc | head -20\"",
			},
		}),
	)
}

func (t ReadOnlyTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	cmd := strings.TrimSpace(args.Command)
	if cmd == "" {
		return registry.Result{}, fmt.Errorf("command is required")
	}
	if err := validateShellCommand(cmd); err != nil {
		return registry.Result{}, err
	}
	if err := t.Guard.Check(cmd); err != nil {
		return registry.Result{}, err
	}
	if err := t.validatePaths(cmd); err != nil {
		return registry.Result{}, err
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sh := exec.CommandContext(ctx, "sh", "-c", t.expandBinaries(cmd))
	if len(t.WorkspaceRoots) > 0 {
		sh.Dir = t.WorkspaceRoots[0]
	}
	var stdout, stderr bytes.Buffer
	sh.Stdout = &stdout
	sh.Stderr = &stderr
	err := sh.Run()
	out := strings.TrimRight(stdout.String(), "\n")
	errOut := strings.TrimSpace(stderr.String())
	if err != nil {
		if errOut == "" {
			errOut = err.Error()
		}
		return registry.Result{}, fmt.Errorf("command failed: %s", truncateOutput(errOut))
	}
	if out == "" && errOut != "" {
		out = errOut
	}
	if out == "" {
		out = "(command completed with no output)"
	}
	return registry.Result{Content: truncateOutput(out)}, nil
}

// expandBinaries rewrites well-known binary names to their configured paths so
// the shell receives the correct executable even when the PATH differs.
func (t ReadOnlyTool) expandBinaries(cmd string) string {
	replacements := map[string]string{}
	if t.GCloudPath != "" {
		replacements["gcloud"] = t.GCloudPath
	}
	if t.KubectlPath != "" {
		replacements["kubectl"] = t.KubectlPath
	}
	if len(replacements) == 0 {
		return cmd
	}
	for name, path := range replacements {
		if filepath.Base(path) == name {
			continue
		}
		cmd = strings.ReplaceAll(cmd, name+" ", path+" ")
		if strings.HasSuffix(cmd, name) {
			cmd = cmd[:len(cmd)-len(name)] + path
		}
	}
	return cmd
}

// stripSingleQuotedStrings removes the content of single-quoted strings only.
// Single-quoted strings are fully literal in POSIX shell: no variable
// expansion, no command substitution. Double-quoted strings are left intact
// because the shell still expands $(...) and `...` inside them — stripping
// their content would create false negatives for command-injection checks.
func stripSingleQuotedStrings(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != '\'' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// Consume the opening quote, skip content, consume closing quote.
		b.WriteByte('\'')
		i++
		for i < len(s) && s[i] != '\'' {
			i++
		}
		if i < len(s) {
			b.WriteByte('\'')
			i++
		}
	}
	return b.String()
}

// splitPipeline splits cmd on '|' characters that are not enclosed by single
// or double quotes. This prevents a '|' inside a quoted argument (e.g. a jq
// filter like '.items[] | .name') from being treated as a pipe operator.
func splitPipeline(cmd string) []string {
	var segments []string
	inSingle, inDouble := false, false
	start := 0
	for i := 0; i < len(cmd); i++ {
		switch cmd[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '\\':
			// Backslash escapes the next char inside double-quoted strings.
			if inDouble && i+1 < len(cmd) {
				i++
			}
		case '|':
			if !inSingle && !inDouble {
				segments = append(segments, cmd[start:i])
				start = i + 1
			}
		}
	}
	return append(segments, cmd[start:])
}

// validateShellCommand checks that the command (including pipelines) only
// invokes allowed programs and does not contain shell meta-operators that
// enable command chaining or output redirection.
func validateShellCommand(cmd string) error {
	// Strip single-quoted substrings before checking for operators so that
	// characters inside quoted arguments (e.g. '&' in a URL query string
	// like curl '...?foo=1&bar=2') do not produce false positives.
	// Double-quoted content is intentionally left intact: the shell still
	// expands $(...) and backticks inside double quotes, so those patterns
	// must remain detectable.
	unquoted := stripSingleQuotedStrings(cmd)

	// Reject chaining operators and redirections; pipes are the only allowed
	// shell construct so that idiomatic one-liners still work.
	for _, illegal := range []string{"&&", "||", ";;", ";", "&", "`", "$(", ">${", ">>", " > ", " 2>", ">/"} {
		if strings.Contains(unquoted, illegal) {
			return fmt.Errorf("shell meta-operator %q is not allowed; use | for pipelines only", illegal)
		}
	}

	// Validate each segment of a pipeline independently.
	for _, segment := range splitPipeline(cmd) {
		if err := validateSegment(strings.TrimSpace(segment)); err != nil {
			return err
		}
	}
	return nil
}

// validateSegment validates a single pipeline segment (no pipes).
func validateSegment(segment string) error {
	if segment == "" {
		return nil
	}
	fields := strings.Fields(segment)
	if len(fields) == 0 {
		return nil
	}
	bin := filepath.Base(fields[0])
	args := fields[1:]

	switch bin {
	case "git":
		return validateGit(args)
	case "kubectl":
		return validateKubectl(args)
	case "gcloud":
		return validateGCloud(args)
	case "gh":
		return validateGH(args)
	case "helm":
		return validateHelm(args)
	case "grep", "rg", "awk", "sed", "sort", "uniq", "head", "tail", "wc",
		"cat", "ls", "find", "echo", "printf", "date", "tr", "cut",
		"xargs", "tee", "diff", "jq", "yq", "curl", "wget",
		"df", "du", "ps", "uname", "hostname", "which", "env",
		"python3", "python", "node", "go", "ruby":
		// General read/transform utilities. curl/wget are included so the
		// agent can inspect endpoints; they are read-only by default.
		return nil
	}
	return fmt.Errorf("command %q is not in the shell allowlist; allowed: git, kubectl, gcloud, gh, helm, grep/rg/awk/sed/jq, find, cat, ls, head/tail/wc/sort/uniq/cut/tr, curl, date, echo", bin)
}

func validateGit(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("git requires a subcommand")
	}
	sub := args[0]
	// Block operations that push or modify remote state, permanently rewrite
	// history, or mutate the working tree. checkout/switch/reset/restore are
	// blocked because all users share the same physical repo directories —
	// switching branches would contaminate other concurrent runs.
	blocked := map[string]bool{
		"push":     true,
		"commit":   true,
		"merge":    true,
		"rebase":   true,
		"am":       true,
		"apply":    true,
		"bisect":   false, // allow (read-only investigation)
		"clean":    true,
		"rm":       true,
		"mv":       true,
		"submodule": false,
		"checkout": true,
		"switch":   true,
		"reset":    true,
		"restore":  true,
		"stash":    true,
		"worktree": true,
	}
	if blocked[sub] {
		return fmt.Errorf("git %s modifies repository state and is not allowed", sub)
	}
	return nil
}

func validateKubectl(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("kubectl requires a subcommand")
	}
	switch args[0] {
	case "get", "describe", "logs", "top", "explain", "api-resources", "api-versions",
		"version", "cluster-info", "rollout":
		if len(args) >= 2 && isSecretResource(args[1]) {
			return fmt.Errorf("kubectl %s secret is blocked", args[0])
		}
		return nil
	case "config":
		if len(args) >= 2 && oneOf(args[1], "current-context", "get-contexts", "view", "use-context") {
			if args[1] == "view" && !contains(args[2:], "--minify") {
				return fmt.Errorf("kubectl config view requires --minify")
			}
			return nil
		}
	}
	return fmt.Errorf("kubectl %s is not in the allowlist (allowed: get, describe, logs, top, config, rollout, explain, version, cluster-info)", args[0])
}

func validateGCloud(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("gcloud requires a group and subcommand")
	}
	if containsMutationFlag(args) {
		return fmt.Errorf("gcloud command contains a write/mutation flag")
	}

	// Strip global flags (--project, --region, --zone, --format, --filter,
	// --limit, --sort-by, --flatten, --verbosity, --log-http, --quiet, etc.)
	// so that "gcloud run services list --project=foo" parses correctly as
	// group="run" resource="services" sub="list".
	stripped := stripGlobalFlags(args)
	if len(stripped) < 2 {
		return fmt.Errorf("gcloud requires a group and subcommand after flags")
	}

	group := stripped[0]
	rest := stripped[1:]
	readSubs := map[string]bool{
		"list": true, "describe": true, "read": true,
		"log": true, "get-health": true, "view": true, "get": true,
		"get-iam-policy": true,
	}

	switch group {
	case "logging":
		if len(rest) >= 1 && oneOf(rest[0], "read", "logs", "list") {
			return nil
		}
	case "container":
		// gcloud container clusters list/describe
		// gcloud container node-pools list/describe
		// gcloud container images list/describe
		if len(rest) >= 2 && readSubs[rest[1]] {
			return nil
		}
	case "run":
		// gcloud run services list/describe
		// gcloud run revisions list/describe
		// gcloud run jobs list/describe
		if len(rest) >= 2 && readSubs[rest[1]] {
			return nil
		}
	case "builds":
		if len(rest) >= 1 && readSubs[rest[0]] {
			return nil
		}
	case "compute":
		// gcloud compute instances list/describe, forwarding-rules, etc.
		if len(rest) >= 2 && readSubs[rest[1]] {
			return nil
		}
	case "projects":
		if len(rest) >= 1 && readSubs[rest[0]] {
			return nil
		}
	case "artifacts":
		if len(rest) >= 2 && readSubs[rest[1]] {
			return nil
		}
	case "monitoring":
		// gcloud monitoring dashboards list/describe
		// gcloud monitoring metrics list
		if len(rest) >= 2 && readSubs[rest[1]] {
			return nil
		}
		if len(rest) >= 1 && readSubs[rest[0]] {
			return nil
		}
	case "iam":
		// gcloud iam service-accounts list/describe
		if len(rest) >= 2 && readSubs[rest[1]] {
			return nil
		}
	case "config":
		if len(rest) >= 1 && oneOf(rest[0], "list", "get-value") {
			return nil
		}
	case "info":
		return nil
	case "version":
		return nil
	}
	return fmt.Errorf("gcloud %s is not read-only allowlisted", group)
}

// stripGlobalFlags removes positional-flag pairs and single-token --flag=value
// arguments that are common gcloud globals so that resource/sub parsing works
// regardless of flag placement.
func stripGlobalFlags(args []string) []string {
	globalFlags := map[string]bool{
		"--project": true, "--region": true, "--zone": true,
		"--format": true, "--filter": true, "--limit": true,
		"--sort-by": true, "--flatten": true, "--verbosity": true,
		"--account": true, "--configuration": true, "--impersonate-service-account": true,
	}
	result := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--") {
			// --flag=value: skip only this token.
			if strings.Contains(arg, "=") {
				continue
			}
			// --flag value: skip flag and next token.
			if globalFlags[arg] {
				i++
				continue
			}
			// --quiet, --log-http, --no-user-output-enabled etc.: single booleans.
			if oneOf(arg, "--quiet", "--log-http", "--no-user-output-enabled", "--user-output-enabled") {
				continue
			}
			// Unknown --flag: keep it (may be resource-specific).
		}
		result = append(result, arg)
	}
	return result
}

func validateGH(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("gh requires a resource and subcommand")
	}
	if containsMutationFlag(args[1:]) {
		return fmt.Errorf("gh command contains a write/mutation flag")
	}
	resource := args[0]
	sub := args[1]
	readSubs := map[string]bool{"list": true, "view": true, "checks": true, "diff": true, "log": true, "status": true}
	switch resource {
	case "run", "workflow", "pr", "issue", "release", "repo", "api":
		if readSubs[sub] {
			return nil
		}
	}
	return fmt.Errorf("gh %s %s is not read-only allowlisted", resource, sub)
}

func validateHelm(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("helm requires a subcommand")
	}
	readSubs := map[string]bool{"list": true, "status": true, "history": true, "get": true, "show": true, "search": true, "version": true, "env": true}
	if readSubs[args[0]] {
		return nil
	}
	return fmt.Errorf("helm %s is not in the allowlist (allowed: list, status, history, get, show, search, version)", args[0])
}

func containsMutationFlag(args []string) bool {
	for i, arg := range args {
		if strings.HasPrefix(arg, "--method=") && !strings.EqualFold(strings.TrimPrefix(arg, "--method="), "GET") {
			return true
		}
		if arg == "--method" && i+1 < len(args) && !strings.EqualFold(args[i+1], "GET") {
			return true
		}
		// --delete and --force are clear mutation signals; --async is used by
		// write operations. --quiet is NOT a mutation flag (it suppresses output
		// on reads too), so it is intentionally excluded here.
		if oneOf(arg, "--delete", "--force", "--async") {
			return true
		}
	}
	return false
}

func isSecretResource(resource string) bool {
	resource = strings.ToLower(strings.TrimSpace(resource))
	resource = strings.TrimSuffix(resource, "s")
	return resource == "secret"
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func truncateOutput(out string) string {
	if len(out) <= maxOutputBytes {
		return out
	}
	return out[:maxOutputBytes] + "\n...[truncated after " + strconv.Itoa(maxOutputBytes) + " bytes]"
}

// validatePaths rejects commands that reference absolute paths outside of the
// configured workspace roots. This prevents agents from inadvertently reading
// code or files from unrelated repositories on the same host.
//
// The check is best-effort: it scans every whitespace-delimited token that
// looks like an absolute path (/…) and verifies it falls under at least one
// workspace root. Tokens that are flags (--foo) or non-path strings are
// skipped. When no WorkspaceRoots are configured the check is a no-op.
//
// /tmp and /var/folders are always allowed as transient scratch space.
func (t ReadOnlyTool) validatePaths(cmd string) error {
	if len(t.WorkspaceRoots) == 0 {
		return nil
	}
	// Always-allowed prefixes: temp directories used for transient files.
	alwaysAllowed := []string{"/tmp/", "/var/folders/", "/var/tmp/"}
	for _, segment := range strings.Split(cmd, "|") {
		for _, token := range strings.Fields(segment) {
			if !strings.HasPrefix(token, "/") {
				continue
			}
			if strings.HasPrefix(token, "--") {
				continue
			}
			clean := filepath.Clean(token)
			// Allow well-known temp paths unconditionally.
			allowed := false
			for _, prefix := range alwaysAllowed {
				if strings.HasPrefix(clean+"/", prefix) || clean == strings.TrimSuffix(prefix, "/") {
					allowed = true
					break
				}
			}
			if allowed {
				continue
			}
			for _, root := range t.WorkspaceRoots {
				if root == "" {
					continue
				}
				rootClean := filepath.Clean(root)
				if clean == rootClean || strings.HasPrefix(clean, rootClean+string(filepath.Separator)) {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("path %q is outside the allowed workspace roots; only paths under %v are permitted", token, t.WorkspaceRoots)
			}
		}
	}
	return nil
}

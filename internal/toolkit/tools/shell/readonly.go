package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// ReadOnlyTool executes a deliberately small command language (including
// pipelines) for operational investigation. It never invokes a shell: doing
// so would make an LLM-provided command a shell program, not a read-only
// diagnostic request.
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
		"Run a read-only diagnostic command and return its stdout. "+
			"Supports pipelines with | so you can compose commands like "+
			"\"kubectl get pods -n mt-prod | grep web\" or "+
			"\"git -C /path/to/repo log --oneline | head -20\". "+
			"Allowed commands: git (all read subcommands: log, blame, diff, grep, show, fetch, ls-files, etc.), "+
			"kubectl (get, describe, logs, top, config), gcloud (list/describe), "+
			"gh (run/pr/issue list and view), "+
			"grep, rg, jq, cat, ls, head, tail, wc, sort, uniq, tr, cut, diff, date, echo, printf. "+
			"No shell expansion, redirection, interpreters, curl/wget, awk/sed, xargs, tee, find -exec, or write operations are supported. "+
			"Quote arguments containing spaces; use | only for pipelines.",
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
	pipeline, err := parsePipeline(cmd)
	if err != nil {
		return registry.Result{}, err
	}
	if err := validatePipeline(pipeline); err != nil {
		return registry.Result{}, err
	}
	if err := t.Guard.Check(cmd); err != nil {
		return registry.Result{}, err
	}
	if err := t.validatePipelinePaths(pipeline); err != nil {
		return registry.Result{}, err
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, errOut, err := t.runPipeline(ctx, pipeline)
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

func (t ReadOnlyTool) binary(name string) string {
	if name == "gcloud" && t.GCloudPath != "" {
		return t.GCloudPath
	}
	if name == "kubectl" && t.KubectlPath != "" {
		return t.KubectlPath
	}
	return name
}

func (t ReadOnlyTool) runPipeline(ctx context.Context, pipeline [][]string) (string, string, error) {
	var input io.Reader
	var stderr bytes.Buffer
	for i, fields := range pipeline {
		cmd := exec.CommandContext(ctx, t.binary(fields[0]), fields[1:]...)
		if len(t.WorkspaceRoots) > 0 {
			cmd.Dir = t.WorkspaceRoots[0]
		}
		cmd.Stdin = input
		cmd.Stderr = &stderr
		var output bytes.Buffer
		cmd.Stdout = &output
		if err := cmd.Run(); err != nil {
			return "", strings.TrimSpace(stderr.String()), err
		}
		if i == len(pipeline)-1 {
			return strings.TrimRight(output.String(), "\n"), strings.TrimSpace(stderr.String()), nil
		}
		input = bytes.NewReader(output.Bytes())
	}
	return "", strings.TrimSpace(stderr.String()), nil
}

// validateShellCommand checks that the command (including pipelines) only
// invokes allowed programs and does not contain shell meta-operators that
// enable command chaining or output redirection.
func validateShellCommand(cmd string) error {
	pipeline, err := parsePipeline(cmd)
	if err != nil {
		return err
	}
	return validatePipeline(pipeline)
}

func validatePipeline(pipeline [][]string) error {
	for _, fields := range pipeline {
		if err := validateSegmentFields(fields); err != nil {
			return err
		}
	}
	return nil
}

// parsePipeline is intentionally a lexer, not a shell parser. Quotes and
// backslash escapes only group literal arguments; there is no substitution,
// glob expansion, redirection, variable expansion, or command chaining.
func parsePipeline(cmd string) ([][]string, error) {
	if strings.Contains(cmd, "$(") || strings.Contains(cmd, "${") {
		return nil, fmt.Errorf("shell substitution is not allowed")
	}
	var pipeline [][]string
	var fields []string
	var word strings.Builder
	inSingle, inDouble, escaped := false, false, false
	flushWord := func() {
		if word.Len() > 0 {
			fields = append(fields, word.String())
			word.Reset()
		}
	}
	flushSegment := func() error {
		flushWord()
		if len(fields) == 0 {
			return fmt.Errorf("empty pipeline segment")
		}
		pipeline = append(pipeline, fields)
		fields = nil
		return nil
	}
	for _, r := range cmd {
		if escaped {
			word.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && !inSingle {
			escaped = true
			continue
		}
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			} else {
				word.WriteRune(r)
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			} else {
				word.WriteRune(r)
			}
		case '|':
			if inSingle || inDouble {
				word.WriteRune(r)
				continue
			}
			if err := flushSegment(); err != nil {
				return nil, err
			}
		case ';', '&', '`', '>', '<':
			if inSingle || inDouble {
				word.WriteRune(r)
				continue
			}
			return nil, fmt.Errorf("shell operator %q is not allowed", string(r))
		case ' ', '\t', '\n':
			if inSingle || inDouble {
				word.WriteRune(r)
			} else {
				flushWord()
			}
		default:
			word.WriteRune(r)
		}
	}
	if escaped || inSingle || inDouble {
		return nil, fmt.Errorf("unterminated escape or quote")
	}
	if err := flushSegment(); err != nil {
		return nil, err
	}
	return pipeline, nil
}

// validateSegment validates a single pipeline segment (no pipes).
func validateSegment(segment string) error {
	pipeline, err := parsePipeline(segment)
	if err != nil {
		return err
	}
	if len(pipeline) != 1 {
		return fmt.Errorf("pipeline is not allowed in a segment")
	}
	return validateSegmentFields(pipeline[0])
}

func validateSegmentFields(fields []string) error {
	if len(fields) == 0 {
		return fmt.Errorf("empty command")
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
	case "grep", "rg", "sort", "uniq", "head", "tail", "wc",
		"cat", "ls", "find", "echo", "printf", "date", "tr", "cut",
		"diff", "jq", "yq", "df", "du", "ps", "uname", "hostname", "which":
		if bin == "find" {
			return fmt.Errorf("find is not supported; use rg or a repository-specific code tool")
		}
		if bin == "jq" || bin == "yq" {
			return validateJSONQuery(args)
		}
		return nil
	}
	return fmt.Errorf("command %q is not in the shell allowlist; allowed: git, kubectl, gcloud, gh, helm, grep/rg/awk/sed/jq, find, cat, ls, head/tail/wc/sort/uniq/cut/tr, curl, date, echo", bin)
}

func validateJSONQuery(args []string) error {
	for _, arg := range args {
		if oneOf(arg, "--rawfile", "--slurpfile", "--from-file", "-f") || strings.HasPrefix(arg, "--rawfile=") || strings.HasPrefix(arg, "--slurpfile=") {
			return fmt.Errorf("jq/yq file-loading options are not allowed")
		}
	}
	return nil
}

func validateGit(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("git requires a subcommand")
	}
	for _, arg := range args {
		if arg == "-c" || arg == "--config-env" || strings.HasPrefix(arg, "-c") || strings.HasPrefix(arg, "--upload-pack") || strings.HasPrefix(arg, "--receive-pack") {
			return fmt.Errorf("git configuration or custom transport options are not allowed")
		}
		if arg == "--git-dir" || strings.HasPrefix(arg, "--git-dir=") || arg == "--work-tree" || strings.HasPrefix(arg, "--work-tree=") ||
			arg == "--namespace" || strings.HasPrefix(arg, "--namespace=") || strings.HasPrefix(arg, "--exec-path") {
			return fmt.Errorf("git repository/environment override option %q is not allowed", arg)
		}
	}
	sub, err := gitSubcommand(args)
	if err != nil {
		return err
	}
	// Block operations that push or modify remote state, permanently rewrite
	// history, or mutate the working tree. checkout/switch/reset/restore are
	// blocked because all users share the same physical repo directories —
	// switching branches would contaminate other concurrent runs.
	blocked := map[string]bool{
		"push":      true,
		"commit":    true,
		"merge":     true,
		"rebase":    true,
		"am":        true,
		"apply":     true,
		"bisect":    false, // allow (read-only investigation)
		"clean":     true,
		"rm":        true,
		"mv":        true,
		"submodule": false,
		"checkout":  true,
		"switch":    true,
		"reset":     true,
		"restore":   true,
		"stash":     true,
		"worktree":  true,
	}
	if blocked[sub] {
		return fmt.Errorf("git %s modifies repository state and is not allowed", sub)
	}
	return nil
}

func gitSubcommand(args []string) (string, error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-C":
			i++
			if i >= len(args) {
				return "", fmt.Errorf("git -C requires a path")
			}
		case strings.HasPrefix(arg, "-C") && arg != "-C":
			return "", fmt.Errorf("git -C must pass the path as a separate argument")
		case arg == "--no-pager" || arg == "--no-optional-locks" || arg == "--bare":
			continue
		case strings.HasPrefix(arg, "--"):
			return "", fmt.Errorf("git global option %q is not allowed", arg)
		case strings.HasPrefix(arg, "-"):
			return "", fmt.Errorf("git global option %q is not allowed", arg)
		default:
			return arg, nil
		}
	}
	return "", fmt.Errorf("git requires a subcommand")
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
		if len(args) >= 2 && oneOf(args[1], "current-context", "get-contexts", "view") {
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
	pipeline, err := parsePipeline(cmd)
	if err != nil {
		return err
	}
	return t.validatePipelinePaths(pipeline)
}

func (t ReadOnlyTool) validatePipelinePaths(pipeline [][]string) error {
	if len(t.WorkspaceRoots) == 0 {
		return nil
	}
	// Always-allowed prefixes: temp directories used for transient files.
	alwaysAllowed := []string{"/tmp/", "/var/folders/", "/var/tmp/"}
	for _, segment := range pipeline {
		for _, token := range segment[1:] {
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

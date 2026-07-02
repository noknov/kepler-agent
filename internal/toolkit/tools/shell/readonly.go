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
type ReadOnlyTool struct {
	GCloudPath  string
	KubectlPath string
	Guard       safety.CommandPolicy
	Timeout     time.Duration
}

func (t ReadOnlyTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"shell",
		"",
		registry.ObjectSchema([]string{"command"}, map[string]any{
			"command": map[string]any{"type": "string", "description": ""},
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

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sh := exec.CommandContext(ctx, "sh", "-c", t.expandBinaries(cmd))
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

// validateShellCommand checks that the command (including pipelines) only
// invokes allowed programs and does not contain shell meta-operators that
// enable command chaining or output redirection.
func validateShellCommand(cmd string) error {
	// Reject chaining operators and redirections; pipes are the only allowed
	// shell construct so that idiomatic one-liners still work.
	for _, illegal := range []string{"&&", "||", ";;", ";", "&", "`", "$(", ">${", ">>", " > ", " 2>", ">/"} {
		if strings.Contains(cmd, illegal) {
			return fmt.Errorf("shell meta-operator %q is not allowed; use | for pipelines only", illegal)
		}
	}

	// Validate each segment of a pipeline independently.
	for _, segment := range strings.Split(cmd, "|") {
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
	// Block operations that push or modify remote state or permanently
	// rewrite history.
	blocked := map[string]bool{
		"push":    true,
		"commit":  true,
		"merge":   true,
		"rebase":  true,
		"am":      true,
		"apply":   true,
		"bisect":  false, // allow (read-only investigation)
		"clean":   true,
		"rm":      true,
		"mv":      true,
		"submodule": false,
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
	if containsMutationFlag(args[1:]) {
		return fmt.Errorf("gcloud command contains a write/mutation flag")
	}
	group := args[0]
	sub := args[1]
	readSubs := map[string]bool{"list": true, "describe": true, "read": true, "log": true, "get-health": true, "view": true}
	switch group {
	case "logging":
		if sub == "read" {
			return nil
		}
	case "container":
		if len(args) >= 3 && readSubs[args[2]] {
			return nil
		}
	case "run":
		if len(args) >= 3 && readSubs[args[2]] {
			return nil
		}
	case "builds":
		if readSubs[sub] {
			return nil
		}
	case "compute":
		if len(args) >= 3 && readSubs[args[2]] {
			return nil
		}
	case "projects":
		if readSubs[sub] {
			return nil
		}
	case "artifacts":
		if len(args) >= 3 && readSubs[args[2]] {
			return nil
		}
	case "config":
		if oneOf(sub, "list", "get", "set") {
			return nil
		}
	}
	return fmt.Errorf("gcloud %s %s is not read-only allowlisted", group, sub)
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
		if oneOf(arg, "--delete", "--force", "--quiet", "--async") {
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

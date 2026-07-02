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

type ReadOnlyTool struct {
	GCloudPath  string
	KubectlPath string
	Guard       safety.CommandPolicy
	Timeout     time.Duration
}

func (ReadOnlyTool) Repeatable() bool { return true }

func (t ReadOnlyTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"readonly-shell",
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
	argv, err := splitCommand(args.Command)
	if err != nil {
		return registry.Result{}, err
	}
	if len(argv) == 0 {
		return registry.Result{}, fmt.Errorf("command is required")
	}
	if err := validateReadOnlyCommand(argv); err != nil {
		return registry.Result{}, err
	}
	bin := t.resolveBinary(argv[0])
	display := bin + " " + strings.Join(argv[1:], " ")
	if err := t.Guard.Check(display); err != nil {
		return registry.Result{}, err
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	out := strings.TrimRight(stdout.String(), "\n")
	errOut := strings.TrimSpace(stderr.String())
	if err != nil {
		if errOut == "" {
			errOut = err.Error()
		}
		return registry.Result{}, fmt.Errorf("%s failed: %s", argv[0], truncateOutput(errOut))
	}
	if out == "" && errOut != "" {
		out = errOut
	}
	if out == "" {
		out = "(command completed with no output)"
	}
	return registry.Result{Content: truncateOutput(out)}, nil
}

func (t ReadOnlyTool) resolveBinary(name string) string {
	switch filepath.Base(name) {
	case "gcloud":
		if t.GCloudPath != "" {
			return t.GCloudPath
		}
	case "kubectl":
		if t.KubectlPath != "" {
			return t.KubectlPath
		}
	}
	return name
}

func splitCommand(command string) ([]string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, nil
	}
	if strings.ContainsAny(command, "\n\r;&|`$") {
		return nil, fmt.Errorf("command contains shell syntax; pass a single read-only command without pipes, redirects, substitutions, or command chaining")
	}
	var args []string
	var b strings.Builder
	var quote rune
	escaped := false
	for _, r := range command {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t':
			if b.Len() > 0 {
				args = append(args, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if escaped {
		b.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in command")
	}
	if b.Len() > 0 {
		args = append(args, b.String())
	}
	for _, arg := range args {
		if isShellControlToken(arg) {
			return nil, fmt.Errorf("command contains shell control token %q", arg)
		}
	}
	return args, nil
}

func isShellControlToken(arg string) bool {
	switch arg {
	case ">", ">>", "<", "<<", "2>", "2>>", "&>", "&>>":
		return true
	default:
		return false
	}
}

func validateReadOnlyCommand(argv []string) error {
	name := filepath.Base(argv[0])
	switch name {
	case "date":
		return validateDate(argv)
	case "gcloud":
		return validateGCloud(argv)
	case "kubectl":
		return validateKubectl(argv)
	case "gh":
		return validateGH(argv)
	default:
		return fmt.Errorf("command %q is not in the read-only allowlist; use gcloud, kubectl, gh, or date", name)
	}
}

func validateDate(argv []string) error {
	if len(argv) > 3 {
		return fmt.Errorf("date allows at most two arguments")
	}
	return nil
}

func validateGCloud(argv []string) error {
	if len(argv) < 3 {
		return fmt.Errorf("gcloud command must include a read-only group and subcommand")
	}
	if containsMutationFlag(argv[1:]) {
		return fmt.Errorf("gcloud command contains a write/mutation flag")
	}
	if argv[1] == "logging" && argv[2] == "read" {
		return nil
	}
	if argv[1] == "container" && len(argv) >= 4 && argv[2] == "clusters" && oneOf(argv[3], "list", "describe") {
		return nil
	}
	if argv[1] == "run" && len(argv) >= 4 && argv[2] == "services" && oneOf(argv[3], "list", "describe") {
		return nil
	}
	if argv[1] == "builds" && oneOf(argv[2], "list", "describe", "log") {
		return nil
	}
	if argv[1] == "compute" && len(argv) >= 4 && oneOf(argv[2], "instances", "backend-services", "health-checks") && oneOf(argv[3], "list", "describe", "get-health") {
		return nil
	}
	return fmt.Errorf("gcloud command is not read-only allowlisted")
}

func validateKubectl(argv []string) error {
	if len(argv) < 2 {
		return fmt.Errorf("kubectl command must include a read-only subcommand")
	}
	switch argv[1] {
	case "get", "describe":
		if len(argv) >= 3 && isSecretResource(argv[2]) {
			return fmt.Errorf("kubectl %s secret resources is blocked", argv[1])
		}
		return nil
	case "logs", "top":
		return nil
	case "config":
		if len(argv) >= 3 && oneOf(argv[2], "current-context", "get-contexts", "view") {
			if argv[2] == "view" && !contains(argv[3:], "--minify") {
				return fmt.Errorf("kubectl config view requires --minify")
			}
			return nil
		}
	}
	return fmt.Errorf("kubectl command is not read-only allowlisted")
}

func validateGH(argv []string) error {
	if len(argv) < 3 {
		return fmt.Errorf("gh command must include a read-only resource and subcommand")
	}
	if containsMutationFlag(argv[1:]) {
		return fmt.Errorf("gh command contains a write/mutation flag")
	}
	switch argv[1] {
	case "run":
		if oneOf(argv[2], "list", "view") {
			return nil
		}
	case "workflow":
		if oneOf(argv[2], "list", "view") {
			return nil
		}
	case "pr":
		if oneOf(argv[2], "list", "view", "checks", "diff") {
			return nil
		}
	}
	return fmt.Errorf("gh command is not read-only allowlisted")
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

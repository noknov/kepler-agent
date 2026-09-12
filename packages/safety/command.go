package safety

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// CommandPolicy validates commands executed by tools (git, gcloud, etc.).
// On a shared cloud server, even tool-internal commands should be checked
// for injection attempts — a crafted LLM argument could potentially escape
// the intended command structure.
type CommandPolicy struct {
	deny []*regexp.Regexp
}

// NewCommandPolicy returns the standard policy for cloud assistants.
// It blocks destructive patterns that could appear via argument injection.
func NewCommandPolicy() CommandPolicy {
	patterns := []string{
		`(?i)\brm\s+.*-[^\s]*r[^\s]*f`,            // rm -rf, rm -fr, rm --recursive --force
		`(?i)\brm\s+-rf\b`,                        // explicit rm -rf
		`(?i)\b(terraform|tofu)\s+destroy\b`,      // terraform destroy
		`(?i)\bkubectl\s+delete\b`,                // kubectl delete anything
		`(?i)\bdocker\s+(rm|rmi|stop|kill)\b`,     // docker destructive ops
		`(?i)\b(shutdown|reboot|halt|poweroff)\b`, // system shutdown
		`(?i)\bmkfs\b`,                            // format filesystem
		`(?i)\bdd\s+`,                             // raw disk write
		`(?i)>\s*/dev/sd`,                         // overwrite disk device
		`(?i)\bchmod\s+777\b`,                     // open world-writable
		`(?i)\bcurl\b.*\|\s*(ba)?sh`,              // pipe curl to shell
		`(?i)\bwget\b.*\|\s*(ba)?sh`,              // pipe wget to shell
		`(?i)\|.*sh\s+-c|&&\s*rm\b`,               // shell injection attempts
	}
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		compiled = append(compiled, regexp.MustCompile(p))
	}
	return CommandPolicy{deny: compiled}
}

func NewLocalCommandPolicy() CommandPolicy {
	patterns := []string{
		`(?i)\brm\s+.*-[^\s]*r[^\s]*f`,
		`(?i)\brm\s+-rf\b`,
		`(?i)\b(terraform|tofu)\s+destroy\b`,
		`(?i)\bkubectl\s+delete\b`,
		`(?i)\bdocker\s+(rm|rmi|stop|kill)\b`,
		`(?i)\b(shutdown|reboot|halt|poweroff)\b`,
		`(?i)\bmkfs\b`,
		`(?i)\bdd\s+`,
		`(?i)\bchmod\s+777\b`,
	}
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		compiled = append(compiled, regexp.MustCompile(p))
	}
	return CommandPolicy{deny: compiled}
}

func (g CommandPolicy) Check(command string) error {
	if len(g.deny) == 0 {
		return nil
	}
	compact := strings.Join(strings.Fields(command), " ")
	for _, re := range g.deny {
		if re.MatchString(compact) {
			return fmt.Errorf("command blocked by safety policy: %s", re.String())
		}
	}
	return nil
}

func (g CommandPolicy) CheckArgv(argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	for _, arg := range argv {
		if strings.Contains(arg, "\x00") {
			return fmt.Errorf("command blocked by safety policy: argv contains NUL byte")
		}
	}
	name := strings.ToLower(filepath.Base(argv[0]))
	if (name == "sh" || name == "bash" || name == "zsh") && len(argv) >= 3 && argv[1] == "-c" {
		return g.Check(argv[2])
	}
	command := argvCommand(name, argv[1:])
	if command == "" {
		return nil
	}
	return g.Check(command)
}

// argvCommand keeps matching on executable and subcommand tokens. A data
// argument is never reparsed as shell, so its contents must not trigger policy.
func argvCommand(name string, args []string) string {
	switch name {
	case "rm":
		var recursive, force bool
		for _, arg := range args {
			if strings.HasPrefix(arg, "-") {
				recursive = recursive || strings.Contains(arg, "r") || arg == "--recursive"
				force = force || strings.Contains(arg, "f") || arg == "--force"
			}
		}
		if recursive && force {
			return "rm -rf"
		}
		return "rm"
	case "terraform", "tofu":
		if containsArg(args, "destroy") {
			return name + " destroy"
		}
		return name
	case "kubectl":
		if containsArg(args, "delete") {
			return "kubectl delete"
		}
		return name
	case "docker":
		for _, destructive := range []string{"rm", "rmi", "stop", "kill"} {
			if containsArg(args, destructive) {
				return "docker " + destructive
			}
		}
		return name
	case "chmod":
		if containsArg(args, "777") {
			return "chmod 777"
		}
		return name
	case "shutdown", "reboot", "halt", "poweroff", "mkfs", "dd":
		return name
	default:
		return name
	}
}

func containsArg(args []string, wanted string) bool {
	for _, arg := range args {
		if strings.EqualFold(arg, wanted) {
			return true
		}
	}
	return false
}

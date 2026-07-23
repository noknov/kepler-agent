package safety

import (
	"fmt"
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
		`(?i);|\|.*sh\s+-c|&&\s*rm\b`,             // shell injection attempts
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

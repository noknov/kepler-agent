package safety

import (
	"fmt"
	"regexp"
	"strings"
)

type CommandPolicy struct {
	deny []*regexp.Regexp
}

func NewCommandPolicy() CommandPolicy {
	patterns := []string{
		`(?i)\brm\s+-rf\b`,
		`(?i)\bgit\s+reset\b`,
		`(?i)\bgit\s+checkout\s+--\b`,
		`(?i)\bgit\s+clean\b`,
		`(?i)\bkubectl\s+(delete|apply|patch|scale|cordon|drain)\b`,
		`(?i)\bgcloud\s+(auth|projects\s+delete|compute\s+instances\s+delete)\b`,
		`(?i)\b(terraform|tofu)\s+(apply|destroy)\b`,
		`(?i)\b(helm|kubectl)\s+upgrade\b`,
		`(?i)\b(mongo|psql|mysql)\b.*\b(drop|delete|update|insert)\b`,
		`(?i)\bchmod\s+777\b`,
		`(?i)\bchown\b`,
	}
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		compiled = append(compiled, regexp.MustCompile(p))
	}
	return CommandPolicy{deny: compiled}
}

func (g CommandPolicy) Check(command string) error {
	compact := strings.Join(strings.Fields(command), " ")
	for _, re := range g.deny {
		if re.MatchString(compact) {
			return fmt.Errorf("command blocked by safety policy: %s", re.String())
		}
	}
	return nil
}

package cli

// ANSI styling aligned with Claude Code dark theme (theme.ts).
// True-color sequences are used when color is enabled; callers fall back via paint helpers.

const (
	colorClaude       = "38;2;215;119;87"
	colorClaudeShim   = "38;2;235;159;127"
	colorDim          = "2"
	colorBold         = "1"
	colorError        = "31"
	colorPromptBorder = "38;2;136;136;136"
	colorUserBg       = "48;2;55;55;55"
)

func paintANSI(color bool, value, code string) string {
	if !color {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

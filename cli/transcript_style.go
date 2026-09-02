package cli

import (
	"fmt"
	"io"
	"strings"
)

const userPromptPrefix = "❯ "
const assistantWrapIndent = "\n     "

// writeUserMessage renders a committed user turn (marginTop + grey bar + pointer).
func writeUserMessage(out io.Writer, color bool, cols int, text string) {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return
	}
	if cols < 24 {
		cols = 24
	}
	fmt.Fprintln(out)
	for _, line := range strings.Split(text, "\n") {
		writeUserMessageLine(out, color, cols, line)
	}
}

func writeUserMessageLine(out io.Writer, color bool, cols int, line string) {
	indent := "  "
	plain := indent + userPromptPrefix + line
	padding := ""
	if pad := cols - displayWidth(plain); pad > 0 {
		padding = strings.Repeat(" ", pad)
	}
	if !color {
		fmt.Fprintln(out, plain+padding)
		return
	}
	body := paintANSI(true, userPromptPrefix, colorDim) + line + padding
	fmt.Fprintf(out, "\x1b[%sm%s%s\x1b[0m\n", colorUserBg, indent, body)
}

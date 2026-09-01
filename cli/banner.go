package cli

import (
	"fmt"
	"io"
	"strings"
)

// Big KEPLER wordmark assembled from block characters (not spaced Latin letters).
const keplerMark = `
 ██   ██ ███████ ██████  ██      ███████ ██████
 ██  ██  ██      ██   ██ ██      ██      ██   ██
 █████   █████   ██████  ██      █████   ██████
 ██  ██  ██      ██      ██      ██      ██   ██
 ██   ██ ███████ ██      ███████ ███████ ██   ██
`

func writeKeplerMark(w io.Writer, paint func(string, string) string) {
	for _, line := range strings.Split(strings.Trim(keplerMark, "\n"), "\n") {
		fmt.Fprintln(w, "  "+paint(line, colorClaude))
	}
}

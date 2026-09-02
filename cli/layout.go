package cli

import (
	"strings"
	"unicode"

	"golang.org/x/text/width"
)

func formatPromptFooter(cols int, cwd, modelName string) string {
	left := "  " + cwd + " · " + modelName
	right := "/help"
	gap := cols - displayWidth(left) - displayWidth(right)
	if gap < 2 {
		keep := max(8, cols-displayWidth(right)-6)
		left = "  " + clipWidth(cwd+" · "+modelName, keep)
		gap = cols - displayWidth(left) - displayWidth(right)
		if gap < 1 {
			gap = 1
		}
	}
	return left + strings.Repeat(" ", gap) + right
}

func displayWidth(s string) int {
	n := 0
	for _, r := range stripANSI(s) {
		n += runeWidth(r)
	}
	return n
}

func runeWidth(r rune) int {
	switch width.LookupRune(r).Kind() {
	case width.EastAsianFullwidth, width.EastAsianWide:
		return 2
	default:
		if r == 0 || unicode.IsControl(r) {
			return 0
		}
		return 1
	}
}

func stripANSI(s string) string {
	out := make([]rune, 0, len(s))
	skip := false
	for _, r := range s {
		if r == 0x1b {
			skip = true
			continue
		}
		if skip {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				skip = false
			}
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

func padWidth(s string, inner int) string {
	w := displayWidth(s)
	if w >= inner {
		return trimToWidth(s, inner)
	}
	return s + strings.Repeat(" ", inner-w)
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func clipLines(s string, max int) string {
	if max <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= max {
		return s
	}
	return strings.Join(lines[:max], "\n")
}

func fitLines(s string, height int) string {
	if height <= 0 {
		return ""
	}
	n := lineCount(s)
	if n > height {
		return clipLines(s, height)
	}
	if n < height {
		return s + strings.Repeat("\n", height-n)
	}
	return s
}

func trimToWidth(s string, limit int) string {
	plain := stripANSI(s)
	w := 0
	runes := []rune(plain)
	i := len(runes)
	for i > 0 && w < limit {
		i--
		w += runeWidth(runes[i])
		if w > limit {
			i++
			break
		}
	}
	return string(runes[i:])
}

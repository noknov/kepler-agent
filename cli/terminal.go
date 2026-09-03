package cli

import (
	"strings"
	"unicode"

	"golang.org/x/text/width"
)

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

func clipWidth(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if displayWidth(s) <= limit {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > limit-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}

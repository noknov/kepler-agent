package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
	"golang.org/x/text/width"
)

type promptEditor struct {
	in      *os.File
	out     io.Writer
	color   bool
	history []string
	histIdx int
}

func newPromptEditor(in *os.File, out io.Writer, color bool) *promptEditor {
	return &promptEditor{in: in, out: out, color: color, histIdx: -1}
}

func (e *promptEditor) paint(value, code string) string {
	return paintANSI(e.color, value, code)
}

func (e *promptEditor) Read(ctx context.Context, cwd, modelName string) (string, error) {
	fd := int(e.in.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer func() { _ = term.Restore(fd, old) }()

	cols := 80
	if w, _, sizeErr := term.GetSize(fd); sizeErr == nil && w >= 40 {
		cols = w
	}
	runes := make([]rune, 0, 64)
	cursor := 0
	pending := make([]byte, 0, 4)
	e.histIdx = len(e.history)
	first := true
	e.draw(cols, runes, cursor, cwd, modelName, &first)
	for {
		if ctx.Err() != nil {
			e.clearBox()
			return "", ctx.Err()
		}
		var b [1]byte
		n, readErr := e.in.Read(b[:])
		if n == 0 {
			if readErr != nil {
				e.clearBox()
				return "", readErr
			}
			continue
		}
		ch := b[0]
		switch {
		case ch == 3:
			e.clearBox()
			return "", context.Canceled
		case ch == 4:
			if len(runes) == 0 {
				e.clearBox()
				return "", io.EOF
			}
		case ch == 13:
			line := string(runes)
			e.commit(line)
			if strings.TrimSpace(line) != "" {
				e.history = append(e.history, line)
			}
			return line, nil
		case ch == 127 || ch == 8:
			if cursor > 0 {
				runes = append(runes[:cursor-1], runes[cursor:]...)
				cursor--
				e.draw(cols, runes, cursor, cwd, modelName, &first)
			}
		case ch == 21:
			runes = runes[:0]
			cursor = 0
			e.draw(cols, runes, cursor, cwd, modelName, &first)
		case ch == 27:
			seq := e.readEscape()
			switch seq {
			case "[A":
				runes, cursor = e.hist(-1, runes)
			case "[B":
				runes, cursor = e.hist(1, runes)
			case "[C":
				if cursor < len(runes) {
					cursor++
				}
			case "[D":
				if cursor > 0 {
					cursor--
				}
			}
			e.draw(cols, runes, cursor, cwd, modelName, &first)
		default:
			if ch < 32 {
				continue
			}
			pending = append(pending, ch)
			for len(pending) > 0 && utf8.FullRune(pending) {
				r, size := utf8.DecodeRune(pending)
				pending = pending[size:]
				if r == utf8.RuneError || unicode.IsControl(r) {
					continue
				}
				runes = append(runes[:cursor], append([]rune{r}, runes[cursor:]...)...)
				cursor++
			}
			e.draw(cols, runes, cursor, cwd, modelName, &first)
		}
	}
}

func (e *promptEditor) readEscape() string {
	var buf [2]byte
	n, _ := e.in.Read(buf[:1])
	if n == 0 {
		return ""
	}
	if buf[0] != '[' {
		return string(buf[:1])
	}
	n2, _ := e.in.Read(buf[1:2])
	if n2 == 0 {
		return "["
	}
	return "[" + string(buf[1])
}

func (e *promptEditor) hist(delta int, current []rune) ([]rune, int) {
	if len(e.history) == 0 {
		return current, len(current)
	}
	next := e.histIdx + delta
	if next < 0 {
		next = 0
	}
	if next > len(e.history) {
		next = len(e.history)
	}
	e.histIdx = next
	if next == len(e.history) {
		return nil, 0
	}
	runes := []rune(e.history[next])
	return runes, len(runes)
}

func (e *promptEditor) draw(cols int, runes []rune, cursor int, cwd, modelName string, first *bool) {
	inner := cols
	if inner < 24 {
		inner = 24
	}
	prompt := e.paint("❯", colorClaude) + " "
	body := "  " + prompt
	if len(runes) == 0 {
		body += e.paint("Type a message…", colorDim)
	} else {
		body += string(runes)
	}
	body = padWidth(body, inner)
	top := e.paint("╭"+strings.Repeat("─", inner-2)+"╮", colorPromptBorder)
	bot := e.paint("╰"+strings.Repeat("─", inner-2)+"╯", colorPromptBorder)
	foot := e.paint(formatPromptFooter(cols, cwd, modelName), colorDim)
	if !*first {
		fmt.Fprint(e.out, "\x1b[1A\r")
	}
	*first = false
	fmt.Fprint(e.out, "\x1b[?25l")
	fmt.Fprintf(e.out, "\r%s\n\r%s\n\r%s\n\r%s\n", top, body, bot, foot)
	col := 2 + displayWidth("❯ ") + displayWidth(string(runes[:min(cursor, len(runes))]))
	if col < 1 {
		col = 1
	}
	fmt.Fprintf(e.out, "\x1b[3A\r\x1b[%dG\x1b[?25h", col)
}

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

func (e *promptEditor) commit(line string) {
	// draw() leaves the cursor on the body line (3 up from the footer).
	// Only move 1 line up to the box top; 4A would jump into prior transcript.
	fmt.Fprint(e.out, "\x1b[1A\r\x1b[0J")
	if strings.TrimSpace(line) == "" {
		return
	}
	if e.color {
		fmt.Fprintf(e.out, "  %s %s %s\n", "\x1b["+colorUserBg+"m", line, "\x1b[0m")
	} else {
		fmt.Fprintf(e.out, "  %s\n", line)
	}
}

func (e *promptEditor) clearBox() {
	fmt.Fprint(e.out, "\x1b[1A\r\x1b[0J")
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

package cli

import "strings"

// FrameLayout mirrors Claude Code's FullscreenLayout:
//   - terminal height is fixed each frame
//   - scrollable content fills the top (flexGrow)
//   - bottom slot is pinned (flexShrink=0)
//
// See claude-code/src/components/FullscreenLayout.tsx
type FrameLayout struct {
	TermHeight  int
	BottomLines int
}

const defaultBottomLines = 3 // top border, input, bottom border

func (l FrameLayout) scrollHeight() int {
	h := l.TermHeight - l.BottomLines
	if h < 1 {
		return 1
	}
	return h
}

// Render composes the fullscreen frame. scroll is transcript content; bottom is
// the prompt block. Output is always exactly TermHeight lines.
func (l FrameLayout) Render(scroll, bottom string) string {
	zone := renderZone(scroll, l.scrollHeight())
	return zone + "\n" + bottom
}

// renderZone renders exactly zoneHeight lines. Content is top-aligned; overflow
// sticks to the bottom of the scroll region.
func renderZone(content string, zoneHeight int) string {
	if zoneHeight <= 0 {
		return ""
	}
	lines := splitContentLines(content)
	var visible []string
	if len(lines) > zoneHeight {
		visible = lines[len(lines)-zoneHeight:]
	} else {
		visible = lines
	}
	return joinLines(visible, zoneHeight)
}

func splitContentLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

// joinLines writes exactly total lines; missing lines are blank.
func joinLines(lines []string, total int) string {
	if total <= 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < total; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i < len(lines) {
			b.WriteString(lines[i])
		}
	}
	return b.String()
}

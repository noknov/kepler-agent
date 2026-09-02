package cli

import "strings"

// inputZoneLines is the fixed height of the bottom prompt block (top border, input, bottom border).
const inputZoneLines = 3

func splitContentLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

// renderZone renders exactly zoneHeight lines. Content is top-aligned; overflow scrolls
// from the bottom. Empty lines pad the remainder so the input zone stays pinned to the
// terminal bottom and every frame is exactly zoneHeight lines tall.
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

package slack

import (
	"regexp"
	"strings"
)

var (
	reImage      = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reBoldAst    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUnd    = regexp.MustCompile(`__(.+?)__`)
	reStrike     = regexp.MustCompile(`~~(.+?)~~`)
	reHeading    = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	reHR         = regexp.MustCompile(`(?m)^[-*]{3,}$`)
	reTable      = regexp.MustCompile(`(?m)^(\|.+\|)\n(\|[-| :]+\|)\n((?:\|.+\|\n?)+)`)
	reBareUserID = regexp.MustCompile(`(^|[^<A-Za-z0-9_])@([UW][A-Z0-9]{8,})\b`)
	reBlankLines = regexp.MustCompile(`\n{3,}`)
	// Slack code blocks don't support language hints — strip them.
	reFencedLang = regexp.MustCompile("(?m)^```[a-zA-Z0-9_+-]+\\s*$")
)

// MarkdownToMrkdwn converts standard Markdown to Slack's mrkdwn format.
func MarkdownToMrkdwn(md string) string {
	s := md

	s = convertTables(s)

	s = reImage.ReplaceAllString(s, "<$2|$1>")
	s = reLink.ReplaceAllString(s, "<$2|$1>")
	s = reBoldAst.ReplaceAllString(s, "*$1*")
	s = reBoldUnd.ReplaceAllString(s, "*$1*")
	s = reStrike.ReplaceAllString(s, "~$1~")
	s = reHeading.ReplaceAllString(s, "*$1*")
	s = reHR.ReplaceAllString(s, "───")
	s = reBareUserID.ReplaceAllString(s, "$1<@$2>")

	s = reFencedLang.ReplaceAllString(s, "```")
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// convertTables turns markdown tables into Slack-readable key-value lists.
// For 2-column tables: "*key*: value" per row.
// For 3+ columns: "• col1 | col2 | col3" per row with a header line.
func convertTables(s string) string {
	return reTable.ReplaceAllStringFunc(s, func(match string) string {
		lines := strings.Split(strings.TrimSpace(match), "\n")
		if len(lines) < 3 {
			return match
		}

		headers := parseCells(lines[0])
		// lines[1] is the separator, skip
		var rows [][]string
		for _, line := range lines[2:] {
			if strings.TrimSpace(line) == "" {
				continue
			}
			rows = append(rows, parseCells(line))
		}

		if len(headers) == 0 || len(rows) == 0 {
			return match
		}

		var b strings.Builder
		if len(headers) == 2 {
			for _, row := range rows {
				val := ""
				if len(row) > 1 {
					val = row[1]
				}
				if len(row) > 0 {
					b.WriteString("*" + row[0] + "*: " + val + "\n")
				}
			}
		} else {
			b.WriteString("*" + strings.Join(headers, " | ") + "*\n")
			for _, row := range rows {
				b.WriteString("• " + strings.Join(row, " | ") + "\n")
			}
		}
		return strings.TrimRight(b.String(), "\n")
	})
}

func parseCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(p))
	}
	return cells
}

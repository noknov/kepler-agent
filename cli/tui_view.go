package cli

import (
	"strings"
	"time"
)

func (m *replModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}
	m.layout.TermHeight = m.height
	return m.layout.Render(m.scrollableContent(), m.renderBottom())
}

func (m *replModel) scrollableContent() string {
	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString(m.transcript.String())
	if m.approval != nil {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(m.renderApproval())
	}
	if m.busy {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(m.renderSpinner())
	}
	return b.String()
}

func (m *replModel) renderBottom() string {
	w := m.width
	if w < 1 {
		w = 80
	}
	border := strings.Repeat("─", max(0, w-2))
	lines := []string{
		m.styles.border().Render("╭" + border + "╮"),
		"  " + m.input.View(),
		m.styles.border().Render("╰" + border + "╯"),
	}
	return joinLines(lines, m.layout.BottomLines)
}

func (m *replModel) renderHeader() string {
	user := m.creds.UserID
	if user == "" {
		user = "session"
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.Trim(keplerMark, "\n"), "\n") {
		b.WriteString("  ")
		b.WriteString(m.styles.claude().Render(line))
		b.WriteString("\n")
	}
	b.WriteString("\n  ")
	b.WriteString(m.styles.dim().Render(m.cwd))
	b.WriteString("\n  ")
	b.WriteString(m.styles.dim().Render(m.config.Model + " · " + user))
	b.WriteString("\n")
	return b.String()
}

func (m *replModel) appendUser(text string) {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return
	}
	m.transcript.WriteString("\n")
	for _, line := range strings.Split(text, "\n") {
		m.appendUserLine(line)
	}
}

func (m *replModel) appendUserLine(line string) {
	w := m.width
	if w < 24 {
		w = 24
	}
	plain := "  " + userPromptPrefix + line
	padding := ""
	if pad := w - displayWidth(plain); pad > 0 {
		padding = strings.Repeat(" ", pad)
	}
	if !m.styles.color {
		m.transcript.WriteString(plain + padding + "\n")
		return
	}
	body := m.styles.dim().Render(userPromptPrefix) + line + padding
	m.transcript.WriteString(m.styles.userBG().Render("  "+body) + "\n")
}

func (m *replModel) renderSpinner() string {
	frames := pingPong(spinnerGlyphs)
	glyph := frames[m.spinnerFrame%len(frames)]
	msg := m.spinnerVerb + "…"
	if m.styles.color {
		return "  " + m.styles.claude().Render(glyph) + " " + glimmerText(msg, time.Now(), colorClaude, colorClaudeShim)
	}
	return "  " + glyph + " " + msg
}

func (m *replModel) renderApproval() string {
	if m.approval == nil {
		return ""
	}
	q := m.approval
	return m.styles.error().Render("!") + " " + q.request.Call.Name + "\n  " +
		m.styles.dim().Render(q.decision.Reason) + "\n  " +
		m.styles.dim().Render("[o]nce [s]ession [p]roject [n]o")
}

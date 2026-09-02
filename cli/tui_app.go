package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

// sessionUI is a Claude Code–style fullscreen REPL:
//   - alt screen owns the entire terminal
//   - scrollable zone: banner + transcript + spinner (sticky bottom)
//   - fixed bottom zone: prompt input (flexShrink=0)
type sessionUI struct {
	ctx       context.Context
	runner    *agentruntime.Runtime
	session   string
	workspace string
	cwd       string
	fragments []prompt.Fragment
	config    local.Config
	creds     credentials
	questions <-chan approvalQuestion
	styles    tuiStyles

	width, height int
	input         textinput.Model

	transcript strings.Builder

	busy         bool
	streamOpen   bool
	toolActivity bool
	toolStarted  map[string]time.Time

	spinnerVerb  string
	spinnerFrame int

	approval *approvalQuestion

	history []string
	histIdx int
	draft   string

	queued []string
}

type spinnerTickMsg struct{}

func runSessionUI(
	ctx context.Context,
	runner *agentruntime.Runtime,
	session, workspace, cwd string,
	fragments []prompt.Fragment,
	config local.Config,
	creds credentials,
	questions <-chan approvalQuestion,
	forward *forwardSink,
) error {
	color := isTerminal(os.Stderr)
	ti := textinput.New()
	ti.Prompt = "❯ "
	ti.Placeholder = "Type a message…"
	ti.Focus()
	ti.CharLimit = 0
	if color {
		ti.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#D77757"))
		ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	}

	m := &sessionUI{
		ctx:         ctx,
		runner:      runner,
		session:     session,
		workspace:   workspace,
		cwd:         cwd,
		fragments:   fragments,
		config:      config,
		creds:       creds,
		questions:   questions,
		styles:      newTUIStyles(color),
		input:       ti,
		toolStarted: make(map[string]time.Time),
		spinnerVerb: randomSpinnerVerb(),
		histIdx:     -1,
	}

	program := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithContext(ctx),
		tea.WithOutput(os.Stderr),
		tea.WithInput(os.Stdin),
	)
	if forward != nil {
		forward.attach(func(msg agentEventMsg) { program.Send(msg) })
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case q, ok := <-questions:
				if !ok {
					return
				}
				program.Send(approvalRequestMsg{question: q})
			}
		}
	}()

	_, err := program.Run()
	if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (m *sessionUI) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinnerTick())
}

func (m *sessionUI) spinnerTick() tea.Cmd {
	if !m.busy {
		return nil
	}
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

func (m *sessionUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.Width = max(0, m.width-6)
		return m, nil
	case spinnerTickMsg:
		if m.busy {
			m.spinnerFrame++
			return m, m.spinnerTick()
		}
		return m, nil
	case agentEventMsg:
		m.handleAgentEvent(msg.event)
		return m, nil
	case turnDoneMsg:
		m.busy = false
		m.streamOpen = false
		m.toolActivity = false
		m.transcript.WriteString("\n")
		if msg.err != nil {
			m.transcript.WriteString(m.styles.error().Render("  " + msg.err.Error()) + "\n")
		}
		if len(msg.queued) > 0 {
			m.queued = append(m.queued, msg.queued...)
		}
		return m, m.maybeRunQueued()
	case approvalRequestMsg:
		m.approval = &msg.question
		return m, nil
	case queuedInputMsg:
		m.queued = append(m.queued, msg.lines...)
		return m, m.maybeRunQueued()
	case tea.KeyMsg:
		if m.approval != nil {
			return m.handleApprovalKey(msg)
		}
		if m.busy {
			return m, nil
		}
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyCtrlD:
			if m.input.Value() == "" {
				return m, tea.Quit
			}
		case tea.KeyEnter:
			line := strings.TrimSpace(m.input.Value())
			if line == "" {
				return m, nil
			}
			return m, m.submit(line)
		case tea.KeyUp:
			m.historyUp()
			return m, nil
		case tea.KeyDown:
			m.historyDown()
			return m, nil
		}
	}

	if m.busy || m.approval != nil {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *sessionUI) handleApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.approval == nil {
		return m, nil
	}
	q := *m.approval
	m.approval = nil
	q.answer <- parseApproval(msg.String())
	return m, nil
}

func (m *sessionUI) submit(line string) tea.Cmd {
	if line == "/exit" || line == "/quit" {
		return tea.Quit
	}
	if m.handleCommand(line) {
		m.input.SetValue("")
		return nil
	}
	m.appendUser(line)
	m.input.SetValue("")
	m.history = append(m.history, line)
	m.histIdx = -1
	m.draft = ""
	return m.startTurn(line)
}

func (m *sessionUI) handleCommand(input string) bool {
	switch strings.TrimSpace(input) {
	case "/help":
		m.transcript.WriteString("\n  /help   commands\n  /status session\n  /exit   quit\n\n")
	case "/status":
		fmt.Fprintf(&m.transcript, "\n  model     %s\n  protocol  %s/%s\n  gateway   %s\n  slack     %s\n  workspace %s\n  session   %s\n\n",
			m.config.Model, m.config.Provider, m.config.Protocol, m.creds.APIURL, m.creds.UserID, m.workspace, m.session)
	case "/clear":
		m.transcript.Reset()
	default:
		return false
	}
	return true
}

func (m *sessionUI) appendUser(text string) {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return
	}
	m.transcript.WriteString("\n")
	for _, line := range strings.Split(text, "\n") {
		m.appendUserLine(line)
	}
}

func (m *sessionUI) appendUserLine(line string) {
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

func (m *sessionUI) startTurn(input string) tea.Cmd {
	m.busy = true
	m.streamOpen = false
	m.toolActivity = false
	m.spinnerVerb = randomSpinnerVerb()
	m.spinnerFrame = 0
	steering := &agentruntime.InputBuffer{}
	return tea.Batch(m.spinnerTick(), m.runTurn(input, steering))
}

func (m *sessionUI) runTurn(input string, steering *agentruntime.InputBuffer) tea.Cmd {
	return func() tea.Msg {
		result, err := m.runner.RunTurn(m.ctx, turnRequest(m.session, m.workspace, input, m.fragments, steering))
		var queued []string
		for _, pending := range steering.Drain() {
			if text := strings.TrimSpace(pending.Text()); text != "" {
				queued = append(queued, text)
			}
		}
		return turnDoneMsg{result: result, err: err, queued: queued}
	}
}

func (m *sessionUI) maybeRunQueued() tea.Cmd {
	if m.busy || len(m.queued) == 0 {
		return nil
	}
	next := m.queued[0]
	m.queued = m.queued[1:]
	m.appendUser(next)
	return m.startTurn(next)
}

func (m *sessionUI) handleAgentEvent(event transcript.Event) {
	switch event.Type {
	case transcript.ModelStreamed:
		if event.Model != nil && event.Model.Type == model.StreamTextDelta {
			if !m.streamOpen {
				gap := "\n"
				if !m.toolActivity {
					gap = "\n\n"
				}
				m.transcript.WriteString(gap + m.styles.dim().Render("⎿") + " ")
				m.streamOpen = true
			}
			m.transcript.WriteString(strings.ReplaceAll(event.Model.Text, "\n", assistantWrapIndent))
		}
	case transcript.ToolCallStarted:
		if event.ToolCall != nil {
			if m.streamOpen {
				m.transcript.WriteString("\n")
				m.streamOpen = false
			}
			m.toolActivity = true
			m.toolStarted[event.ToolCall.ID] = time.Now()
			summary := toolArgSummary(event.ToolCall.Arguments)
			line := m.styles.claude().Render("⏺") + " " + m.styles.bold().Render(toolDisplayName(event.ToolCall.Name))
			if summary != "" {
				line += m.styles.dim().Render("(" + summary + ")")
			}
			m.transcript.WriteString("  " + line + "\n")
		}
	case transcript.ToolCallCompleted, transcript.ToolCallFailed:
		if event.ToolCall != nil {
			elapsed := time.Since(m.toolStarted[event.ToolCall.ID]).Round(10 * time.Millisecond)
			detail := toolResultSummary(event.ToolResult)
			if event.Type == transcript.ToolCallFailed {
				detail = "failed · " + elapsed.String()
				if event.ToolResult != nil {
					if msg := clipWidth(firstLine(event.ToolResult.Text()), 60); msg != "" {
						detail = msg + " · " + elapsed.String()
					}
				}
				m.transcript.WriteString("    " + m.styles.error().Render("✗ "+detail) + "\n")
			} else {
				if detail == "" {
					detail = elapsed.String()
				} else {
					detail = detail + " · " + elapsed.String()
				}
				m.transcript.WriteString("    " + m.styles.dim().Render("⎿ "+detail) + "\n")
			}
			delete(m.toolStarted, event.ToolCall.ID)
		}
	case transcript.TurnCompleted, transcript.TurnFailed, transcript.TurnCanceled:
		m.streamOpen = false
		m.toolActivity = false
	}
}

func (m *sessionUI) scrollableContent() string {
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

func (m *sessionUI) historyUp() {
	if len(m.history) == 0 {
		return
	}
	if m.histIdx < 0 {
		m.draft = m.input.Value()
		m.histIdx = len(m.history) - 1
	} else if m.histIdx > 0 {
		m.histIdx--
	}
	m.input.SetValue(m.history[m.histIdx])
}

func (m *sessionUI) historyDown() {
	if len(m.history) == 0 || m.histIdx < 0 {
		return
	}
	if m.histIdx < len(m.history)-1 {
		m.histIdx++
		m.input.SetValue(m.history[m.histIdx])
		return
	}
	m.histIdx = -1
	m.input.SetValue(m.draft)
}

// View renders exactly m.height lines every frame: fixed-height scroll zone + input pinned
// to the bottom. Variable-height frames leak into alt-screen scrollback and overlap.
func (m *sessionUI) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}

	scrollH := m.height - inputZoneLines
	if scrollH < 1 {
		scrollH = 1
	}

	scroll := renderZone(m.scrollableContent(), scrollH)
	input := m.renderInputZone()
	return scroll + "\n" + input
}

func (m *sessionUI) renderInputZone() string {
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
	return joinLines(lines, inputZoneLines)
}

func (m *sessionUI) renderHeader() string {
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

func (m *sessionUI) renderSpinner() string {
	frames := pingPong(spinnerGlyphs)
	glyph := frames[m.spinnerFrame%len(frames)]
	msg := m.spinnerVerb + "…"
	if m.styles.color {
		return "  " + m.styles.claude().Render(glyph) + " " + glimmerText(msg, time.Now(), colorClaude, colorClaudeShim)
	}
	return "  " + glyph + " " + msg
}

func (m *sessionUI) renderApproval() string {
	if m.approval == nil {
		return ""
	}
	q := m.approval
	return m.styles.error().Render("!") + " " + q.request.Call.Name + "\n  " +
		m.styles.dim().Render(q.decision.Reason) + "\n  " +
		m.styles.dim().Render("[o]nce [s]ession [p]roject [n]o")
}

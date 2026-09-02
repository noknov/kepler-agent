package cli

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func (m *replModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinnerTick())
}

func (m *replModel) spinnerTick() tea.Cmd {
	if !m.busy {
		return nil
	}
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

func (m *replModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout.TermHeight = msg.Height
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

func (m *replModel) handleApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.approval == nil {
		return m, nil
	}
	q := *m.approval
	m.approval = nil
	q.answer <- parseApproval(msg.String())
	return m, nil
}

func (m *replModel) submit(line string) tea.Cmd {
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

func (m *replModel) handleCommand(input string) bool {
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

func (m *replModel) startTurn(input string) tea.Cmd {
	m.busy = true
	m.streamOpen = false
	m.toolActivity = false
	m.spinnerVerb = randomSpinnerVerb()
	m.spinnerFrame = 0
	steering := &agentruntime.InputBuffer{}
	return tea.Batch(m.spinnerTick(), m.runTurn(input, steering))
}

func (m *replModel) runTurn(input string, steering *agentruntime.InputBuffer) tea.Cmd {
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

func (m *replModel) maybeRunQueued() tea.Cmd {
	if m.busy || len(m.queued) == 0 {
		return nil
	}
	next := m.queued[0]
	m.queued = m.queued[1:]
	m.appendUser(next)
	return m.startTurn(next)
}

func (m *replModel) handleAgentEvent(event transcript.Event) {
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

func (m *replModel) historyUp() {
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

func (m *replModel) historyDown() {
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

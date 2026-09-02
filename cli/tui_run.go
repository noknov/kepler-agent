package cli

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

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
	ti := newPromptInput(color)

	m := &replModel{
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
		layout:      FrameLayout{BottomLines: defaultBottomLines},
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
	go watchApprovalQuestions(ctx, questions, program.Send)

	_, err := program.Run()
	if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func newPromptInput(color bool) textinput.Model {
	ti := textinput.New()
	ti.Prompt = "❯ "
	ti.Placeholder = "Type a message…"
	ti.Focus()
	ti.CharLimit = 0
	if color {
		ti.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#D77757"))
		ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	}
	return ti
}

func watchApprovalQuestions(ctx context.Context, questions <-chan approvalQuestion, send func(tea.Msg)) {
	for {
		select {
		case <-ctx.Done():
			return
		case q, ok := <-questions:
			if !ok {
				return
			}
			send(approvalRequestMsg{question: q})
		}
	}
}

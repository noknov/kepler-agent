package cli

import "github.com/charmbracelet/lipgloss"

type tuiStyles struct {
	color bool
}

func newTUIStyles(color bool) tuiStyles {
	return tuiStyles{color: color}
}

func (s tuiStyles) claude() lipgloss.Style {
	if !s.color {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#D77757"))
}

func (s tuiStyles) dim() lipgloss.Style {
	if !s.color {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
}

func (s tuiStyles) bold() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true)
}

func (s tuiStyles) error() lipgloss.Style {
	if !s.color {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
}

func (s tuiStyles) border() lipgloss.Style {
	if !s.color {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
}

func (s tuiStyles) userBG() lipgloss.Style {
	if !s.color {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Background(lipgloss.Color("#373737"))
}

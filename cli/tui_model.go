package cli

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

// replModel is the bubbletea model for the interactive fullscreen REPL.
type replModel struct {
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
	layout    FrameLayout

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

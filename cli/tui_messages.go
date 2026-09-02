package cli

import (
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type agentEventMsg struct {
	event transcript.Event
}

type turnDoneMsg struct {
	result agentruntime.TurnResult
	err    error
	queued []string
}

type approvalRequestMsg struct {
	question approvalQuestion
}

type queuedInputMsg struct {
	lines []string
}

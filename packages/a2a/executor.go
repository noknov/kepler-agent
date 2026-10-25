package agentremote

import (
	"context"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2asrv"
	"github.com/a2aproject/a2a-go/a2asrv/eventqueue"
	"github.com/noknov/kepler-agent/packages/agent/delegation"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
)

// TaskRunner is implemented by delegation.Runner. Workflows and protocol
// bindings share that existing execution primitive, not another agent loop.
type TaskRunner interface {
	RunTask(context.Context, delegation.TaskRequest) (delegation.TaskResult, error)
}

// Executor is the inbound A2A SDK adapter. The composition root must supply
// authenticated scope and authorize all SDK task-store reads/cancels. This
// executor alone is not an HTTP authentication or multi-tenant storage layer.
// No listener, default identity, or memory-only production task store is added.
type Executor struct {
	Runner        TaskRunner
	Scope         func(context.Context) (tool.Scope, error)
	MaxInputBytes int
}

func (e Executor) Execute(ctx context.Context, request *a2asrv.RequestContext, queue eventqueue.Queue) error {
	if e.Runner == nil || e.Scope == nil || request.Message == nil {
		return fmt.Errorf("A2A execution is not configured")
	}
	scope, err := e.Scope(ctx)
	if err != nil || scope.UserID == "" {
		return fmt.Errorf("authenticated A2A scope required")
	}
	limit := e.MaxInputBytes
	if limit <= 0 {
		limit = 64 << 10
	}
	var input strings.Builder
	for _, part := range request.Message.Parts {
		text, ok := part.(a2a.TextPart)
		if !ok {
			return fmt.Errorf("A2A executor accepts text objectives only")
		}
		if len(text.Text) > limit-input.Len() {
			return fmt.Errorf("A2A objective exceeds input budget")
		}
		input.WriteString(text.Text)
	}
	if strings.TrimSpace(input.String()) == "" {
		return fmt.Errorf("A2A objective is required")
	}
	if err := queue.Write(ctx, a2a.NewStatusUpdateEvent(request, a2a.TaskStateWorking, nil)); err != nil {
		return err
	}
	result, runErr := e.Runner.RunTask(ctx, delegation.TaskRequest{Spec: delegation.TaskSpec{Task: input.String()}, Scope: scope})
	if ctx.Err() != nil {
		return nil
	} // SDK cancellation already owns the final state.
	state := a2a.TaskStateCompleted
	if runErr != nil || result.Audit.Termination != agentruntime.TerminationCompleted {
		state = a2a.TaskStateFailed
	} else if result.Message.Text() != "" {
		artifact := a2a.NewArtifactEvent(request, a2a.TextPart{Text: result.Message.Text()})
		if err := queue.Write(ctx, artifact); err != nil {
			return err
		}
	}
	// Do not export local transcript IDs, error internals, reasoning or scope.
	event := a2a.NewStatusUpdateEvent(request, state, nil)
	event.Final = true
	return queue.Write(ctx, event)
}
func (e Executor) Cancel(ctx context.Context, request *a2asrv.RequestContext, queue eventqueue.Queue) error {
	if e.Scope == nil {
		return fmt.Errorf("authenticated A2A scope required")
	}
	scope, err := e.Scope(ctx)
	if err != nil || scope.UserID == "" {
		return fmt.Errorf("authenticated A2A scope required")
	}
	event := a2a.NewStatusUpdateEvent(request, a2a.TaskStateCanceled, nil)
	event.Final = true
	return queue.Write(ctx, event)
}

var _ a2asrv.AgentExecutor = Executor{}

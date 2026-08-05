// Package agentcore contains the transport-neutral agent execution engine.
// Delivery adapters provide context and presentation hooks; Core owns runner
// configuration, cancellation semantics, and protocol lifecycle events.
package agentcore

import (
	"context"
	"errors"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent"
	"github.com/noknov/slack-copilot-agent/packages/agentprotocol"
	"github.com/noknov/slack-copilot-agent/packages/llm"
)

type Core struct {
	Runner agent.Runner
	Events agentprotocol.Sink
}

type Hooks struct {
	Stream          func(agent.StreamEvent)
	Status          func(string)
	LoadingMessage  func(string)
	Usage           func(llm.Usage)
	LLMStepComplete func()
	ToolEvent       func(agent.ToolEvent)
}

type TurnRequest struct {
	ThreadID string
	TurnID   string
	Model    string
	Agent    agent.Request
	Observer agent.Observer
	Hooks    Hooks
	Events   agentprotocol.Sink
}

type TurnResult struct {
	TurnID string
	Agent  agent.Result
}

func (c *Core) Execute(ctx context.Context, req TurnRequest) (TurnResult, error) {
	if req.ThreadID == "" {
		return TurnResult{}, errors.New("thread id is required")
	}
	if req.TurnID == "" {
		req.TurnID = agentprotocol.NewTurnID()
	}
	runner := c.Runner
	if runner.Tools != nil {
		runner.Tools = runner.Tools.Clone()
	}
	if req.Model != "" {
		runner.Model = req.Model
	}
	if req.Observer != nil {
		runner.Observer = req.Observer
	}
	applyHooks(&runner, req.Hooks)
	sink := agentprotocol.MultiSink{c.Events, req.Events}
	publish(ctx, sink, agentprotocol.Event{
		Type: agentprotocol.TurnStarted, ThreadID: req.ThreadID,
		TurnID: req.TurnID, Status: agentprotocol.StatusRunning,
	})

	messageID := req.TurnID + ":assistant"
	messageStarted := false
	stream := runner.OnStream
	runner.OnStream = func(event agent.StreamEvent) {
		if !messageStarted {
			messageStarted = true
			publish(ctx, sink, agentprotocol.Event{
				Type: agentprotocol.ItemStarted, ThreadID: req.ThreadID, TurnID: req.TurnID,
				Status: agentprotocol.StatusRunning,
				Item:   &agentprotocol.Item{ID: messageID, Kind: "message", Status: agentprotocol.StatusRunning, StartedAt: time.Now().UTC()},
			})
		}
		publish(ctx, sink, agentprotocol.Event{
			Type: agentprotocol.ItemDelta, ThreadID: req.ThreadID, TurnID: req.TurnID,
			Status: agentprotocol.StatusRunning,
			Item:   &agentprotocol.Item{ID: messageID, Kind: "message", Status: agentprotocol.StatusRunning, Delta: event.Delta},
		})
		if stream != nil {
			stream(event)
		}
	}
	toolEvent := runner.OnToolEvent
	runner.OnToolEvent = func(event agent.ToolEvent) {
		eventType := agentprotocol.ItemStarted
		status := agentprotocol.StatusRunning
		if event.Phase == "completed" {
			eventType = agentprotocol.ItemCompleted
			status = agentprotocol.StatusCompleted
			if event.Error != nil {
				eventType = agentprotocol.ItemFailed
				status = agentprotocol.StatusFailed
			}
		}
		publish(ctx, sink, agentprotocol.Event{
			Type: eventType, ThreadID: req.ThreadID, TurnID: req.TurnID, Status: status,
			Item: &agentprotocol.Item{
				ID: event.ID, Kind: "tool", Name: event.Name, Status: status,
				StartedAt: event.StartedAt, DurationMS: event.Duration.Milliseconds(), Error: errorText(event.Error),
			},
		})
		if toolEvent != nil {
			toolEvent(event)
		}
	}

	result, err := runner.Run(ctx, req.Agent)
	if !messageStarted && (result.Final != "" || result.PendingQuestion != "") {
		messageStarted = true
		publish(ctx, sink, agentprotocol.Event{
			Type: agentprotocol.ItemStarted, ThreadID: req.ThreadID, TurnID: req.TurnID,
			Status: agentprotocol.StatusRunning,
			Item:   &agentprotocol.Item{ID: messageID, Kind: "message", Status: agentprotocol.StatusRunning, StartedAt: time.Now().UTC()},
		})
	}
	if messageStarted {
		status := agentprotocol.StatusCompleted
		eventType := agentprotocol.ItemCompleted
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			status = agentprotocol.StatusCanceled
			eventType = agentprotocol.ItemCanceled
		} else if err != nil {
			status = agentprotocol.StatusFailed
			eventType = agentprotocol.ItemFailed
		}
		content := result.Final
		if result.Pending && content == "" {
			content = result.PendingQuestion
		}
		publish(ctx, sink, agentprotocol.Event{
			Type: eventType, ThreadID: req.ThreadID, TurnID: req.TurnID, Status: status,
			Item: &agentprotocol.Item{ID: messageID, Kind: "message", Status: status, Content: content, Error: errorText(err)},
		})
	}

	status, eventType := terminalEvent(ctx, result, err)
	event := agentprotocol.Event{Type: eventType, ThreadID: req.ThreadID, TurnID: req.TurnID, Status: status}
	if err != nil {
		event.Error = &agentprotocol.ProtocolError{Code: "turn_failed", Message: err.Error(), Retryable: !errors.Is(err, context.Canceled)}
	}
	publish(context.WithoutCancel(ctx), sink, event)
	return TurnResult{TurnID: req.TurnID, Agent: result}, err
}

func applyHooks(runner *agent.Runner, hooks Hooks) {
	runner.OnStream = hooks.Stream
	runner.StatusUpdate = hooks.Status
	runner.LoadingMessageUpdate = hooks.LoadingMessage
	runner.OnUsage = hooks.Usage
	runner.OnLLMStepComplete = hooks.LLMStepComplete
	runner.OnToolEvent = hooks.ToolEvent
}

func terminalEvent(ctx context.Context, result agent.Result, err error) (agentprotocol.Status, agentprotocol.EventType) {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return agentprotocol.StatusCanceled, agentprotocol.TurnCanceled
	}
	if err != nil {
		return agentprotocol.StatusFailed, agentprotocol.TurnFailed
	}
	if result.Pending {
		return agentprotocol.StatusPendingUser, agentprotocol.TurnCompleted
	}
	return agentprotocol.StatusCompleted, agentprotocol.TurnCompleted
}

func publish(ctx context.Context, sink agentprotocol.Sink, event agentprotocol.Event) {
	if sink != nil {
		sink.Publish(ctx, agentprotocol.Normalize(event))
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

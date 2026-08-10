package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/prompt"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/transcript"
)

var errPendingApproval = errors.New("tool call is waiting for approval")

func (r *Runtime) RunTurn(ctx context.Context, request TurnRequest) (TurnResult, error) {
	if request.SessionID == "" {
		request.SessionID = r.deps.IDs.New("ses")
	}
	if request.TurnID == "" {
		request.TurnID = r.deps.IDs.New("turn")
	}
	if request.Input.Role == "" {
		request.Input.Role = model.RoleUser
	}
	if request.Input.Role != model.RoleUser {
		return TurnResult{}, fmt.Errorf("turn input must have user role")
	}
	request.Scope.SessionID = request.SessionID
	request.Scope.TurnID = request.TurnID
	lock := r.sessionLock(request.SessionID)
	lock.Lock()
	defer lock.Unlock()

	result := TurnResult{SessionID: request.SessionID, TurnID: request.TurnID}
	events, err := r.deps.Transcript.Load(ctx, request.SessionID, 0)
	if err != nil {
		return result, err
	}
	if len(events) == 0 {
		if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, Type: transcript.SessionStarted}); err != nil {
			return result, err
		}
	}
	if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.TurnStarted, Status: "running"}); err != nil {
		return result, err
	}
	if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.UserInput, Message: &request.Input}); err != nil {
		return result, err
	}

	composition, err := prompt.Compose(request.Prompt)
	if err != nil {
		return r.failTurn(ctx, result, err)
	}
	system := model.TextMessage(model.RoleSystem, composition.Content)
	repeated := make(map[string]int)
	for step := 1; step <= r.config.MaxSteps; step++ {
		result.Steps = step
		if err := ctx.Err(); err != nil {
			return r.cancelTurn(result, err)
		}
		if err := r.appendSteering(ctx, request); err != nil {
			return r.failTurn(ctx, result, err)
		}
		projection, err := r.projectContext(ctx, request, system)
		if err != nil {
			return r.failTurn(ctx, result, err)
		}
		metadata, _ := json.Marshal(map[string]any{
			"estimated_tokens": projection.EstimatedTokens, "message_count": len(projection.Messages),
			"prompt_hash": composition.Hash, "tool_count": len(r.deps.Tools.ActiveDefinitions(request.SessionID)),
		})
		if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.ContextProjected, Metadata: metadata}); err != nil {
			return r.failTurn(ctx, result, err)
		}

		response, err := r.generate(ctx, request, projection.Messages)
		if err != nil {
			return r.failTurn(ctx, result, err)
		}
		addUsage(&result.Usage, response.Usage)
		if response.Message.Role == "" {
			response.Message.Role = model.RoleAssistant
		}
		if len(response.Message.Content) == 0 {
			return r.finishTurn(ctx, result, model.Message{}, TerminationEmptyResponse, errors.New("model returned an empty response"))
		}
		if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.AssistantMessage, Message: &response.Message}); err != nil {
			return r.failTurn(ctx, result, err)
		}
		calls := response.Message.ToolCalls()
		if len(calls) == 0 {
			if response.FinishReason == model.FinishLength {
				instruction := model.TextMessage(model.RoleUser, "Continue and finish concisely without repeating the previous response.")
				if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.RuntimeInstruction, Message: &instruction}); err != nil {
					return r.failTurn(ctx, result, err)
				}
				continue
			}
			if count, appendErr := r.appendSteeringCount(ctx, request); appendErr != nil {
				return r.failTurn(ctx, result, appendErr)
			} else if count > 0 {
				continue
			}
			result.Message = response.Message
			return r.finishTurn(ctx, result, response.Message, TerminationCompleted, nil)
		}
		looped, err := r.executeTools(ctx, request, calls, repeated)
		if errors.Is(err, errPendingApproval) {
			return r.finishTurn(ctx, result, response.Message, TerminationPendingApproval, err)
		}
		if err != nil {
			return r.failTurn(ctx, result, err)
		}
		if looped {
			return r.finishTurn(ctx, result, response.Message, TerminationLoopDetected, errors.New("repeated tool-call loop detected"))
		}
	}
	return r.finishTurn(ctx, result, result.Message, TerminationMaxSteps, errors.New("maximum agent steps exceeded"))
}

func (r *Runtime) projectContext(ctx context.Context, request TurnRequest, system model.Message) (Projection, error) {
	events, err := r.deps.Transcript.Load(ctx, request.SessionID, 0)
	if err != nil {
		return Projection{}, err
	}
	projection, err := r.deps.Projector.Project(ctx, events, system)
	if err != nil {
		return Projection{}, err
	}
	if len(projection.Dropped) == 0 || r.deps.Compactor == nil {
		return projection, nil
	}
	summary, err := r.deps.Compactor.Compact(ctx, projection.Dropped, r.config.Context.MaxTokens/4)
	if err != nil {
		return Projection{}, err
	}
	metadata, _ := json.Marshal(map[string]uint64{"covers_through": projection.CoversThrough})
	if _, err = r.record(ctx, transcript.Event{
		SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.CompactionCreated,
		Message: &summary, Metadata: metadata,
	}); err != nil {
		return Projection{}, err
	}
	events, err = r.deps.Transcript.Load(ctx, request.SessionID, 0)
	if err != nil {
		return Projection{}, err
	}
	return r.deps.Projector.Project(ctx, events, system)
}

func (r *Runtime) generate(ctx context.Context, turn TurnRequest, messages []model.Message) (model.Response, error) {
	request := model.Request{
		Model: r.config.Model, Messages: messages, Tools: r.deps.Tools.ActiveDefinitions(turn.SessionID),
		ReasoningEffort: r.config.ReasoningEffort, MaxOutputTokens: r.config.MaxOutputTokens,
		Metadata: map[string]string{"session_id": turn.SessionID, "turn_id": turn.TurnID},
	}
	var lastErr error
	for attempt := 0; attempt <= r.config.MaxModelRetries; attempt++ {
		metadata, _ := json.Marshal(map[string]any{"attempt": attempt + 1, "model": request.Model})
		if _, err := r.record(ctx, transcript.Event{SessionID: turn.SessionID, TurnID: turn.TurnID, Type: transcript.ModelRequested, Metadata: metadata}); err != nil {
			return model.Response{}, err
		}
		response, err := r.deps.Model.Generate(ctx, request, func(event model.StreamEvent) error {
			_, recordErr := r.record(ctx, transcript.Event{SessionID: turn.SessionID, TurnID: turn.TurnID, Type: transcript.ModelStreamed, Model: &event})
			return recordErr
		})
		if err == nil {
			return response, nil
		}
		lastErr = err
		var typed *model.Error
		if !errors.As(err, &typed) || !typed.Retryable || attempt == r.config.MaxModelRetries {
			break
		}
		if err := r.deps.Sleep(ctx, r.config.RetryBaseDelay*time.Duration(1<<attempt)); err != nil {
			return model.Response{}, err
		}
	}
	return model.Response{}, lastErr
}

func (r *Runtime) appendSteering(ctx context.Context, request TurnRequest) error {
	_, err := r.appendSteeringCount(ctx, request)
	return err
}

func (r *Runtime) appendSteeringCount(ctx context.Context, request TurnRequest) (int, error) {
	if request.Steering == nil {
		return 0, nil
	}
	messages := request.Steering.Drain()
	for index := range messages {
		message := messages[index]
		if message.Role == "" {
			message.Role = model.RoleUser
		}
		if _, err := r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.SteeringInput, Message: &message}); err != nil {
			return index, err
		}
	}
	return len(messages), nil
}

func addUsage(total *model.Usage, usage model.Usage) {
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.CacheReadTokens += usage.CacheReadTokens
	total.CacheCreatedTokens += usage.CacheCreatedTokens
}

func (r *Runtime) record(ctx context.Context, event transcript.Event) (transcript.Event, error) {
	if event.ID == "" {
		event.ID = r.deps.IDs.New("evt")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = r.deps.Clock()
	}
	stored, err := r.deps.Transcript.Append(ctx, event)
	if err == nil && r.deps.Events != nil {
		r.deps.Events.Publish(ctx, stored)
	}
	return stored, err
}

func (r *Runtime) finishTurn(ctx context.Context, result TurnResult, message model.Message, reason TerminationReason, err error) (TurnResult, error) {
	result.Message = message
	result.Termination = reason
	eventType := transcript.TurnCompleted
	status := string(reason)
	if err != nil {
		eventType = transcript.TurnFailed
	}
	if reason == TerminationCanceled {
		eventType = transcript.TurnCanceled
	}
	event := transcript.Event{SessionID: result.SessionID, TurnID: result.TurnID, Type: eventType, Status: status}
	if err != nil {
		event.Error = err.Error()
	}
	_, recordErr := r.record(context.WithoutCancel(ctx), event)
	if recordErr != nil && err == nil {
		err = recordErr
	}
	return result, err
}

func (r *Runtime) failTurn(ctx context.Context, result TurnResult, err error) (TurnResult, error) {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return r.cancelTurn(result, err)
	}
	result.Termination = TerminationModelError
	return r.finishTurn(ctx, result, result.Message, result.Termination, err)
}

func (r *Runtime) cancelTurn(result TurnResult, err error) (TurnResult, error) {
	result.Termination = TerminationCanceled
	return r.finishTurn(context.Background(), result, result.Message, result.Termination, err)
}

func toolCallKey(call model.ToolCall) string {
	sum := sha256.Sum256(append([]byte(call.Name+"\x00"), call.Arguments...))
	return hex.EncodeToString(sum[:])
}

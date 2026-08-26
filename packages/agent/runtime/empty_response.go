package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

var errEmptyModelResponse = errors.New("model returned an empty response")

// generateForStep calls the model for one agent step, including context-limit
// recovery and bounded in-place retries when the provider returns an empty
// message with no error.
func (r *Runtime) generateForStep(ctx context.Context, request TurnRequest, system model.Message, messages []model.Message) (model.Response, error) {
	response, err := r.generate(ctx, request, messages)
	if err != nil {
		response, err = r.recoverGenerateAfterContextLimit(ctx, request, system, err)
		if err != nil {
			return model.Response{}, err
		}
	}
	return r.retryEmptyResponse(ctx, request, system, response)
}

func (r *Runtime) recoverGenerateAfterContextLimit(ctx context.Context, request TurnRequest, system model.Message, err error) (model.Response, error) {
	var typed *model.Error
	if !errors.As(err, &typed) || typed.Kind != model.ErrorContextLimit || r.deps.Compactor == nil {
		return model.Response{}, err
	}
	if compactErr := r.forceCompactAfterContextLimit(ctx, request, system); compactErr != nil {
		return model.Response{}, err
	}
	projection, projErr := r.projectContext(ctx, request, system)
	if projErr != nil {
		return model.Response{}, projErr
	}
	response, err := r.generate(ctx, request, projection.Messages)
	if err != nil {
		return model.Response{}, err
	}
	return response, nil
}

func (r *Runtime) retryEmptyResponse(ctx context.Context, request TurnRequest, system model.Message, response model.Response) (model.Response, error) {
	if len(response.Message.Content) > 0 {
		return normalizeAssistantRole(response), nil
	}
	for attempt := 0; attempt < r.config.MaxEmptyResponseRetries; attempt++ {
		metadata, _ := json.Marshal(map[string]any{"attempt": attempt + 1, "reason": "empty_response"})
		if _, err := r.record(ctx, transcript.Event{
			SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.ModelFailed,
			Error: errEmptyModelResponse.Error(), Metadata: metadata,
		}); err != nil {
			return model.Response{}, err
		}
		if err := r.deps.Sleep(ctx, r.config.RetryBaseDelay*time.Duration(1<<attempt)); err != nil {
			return model.Response{}, err
		}
		projection, err := r.projectContext(ctx, request, system)
		if err != nil {
			return model.Response{}, err
		}
		retry, err := r.generate(ctx, request, projection.Messages)
		if err != nil {
			retry, err = r.recoverGenerateAfterContextLimit(ctx, request, system, err)
			if err != nil {
				return model.Response{}, err
			}
		}
		if len(retry.Message.Content) > 0 {
			return normalizeAssistantRole(retry), nil
		}
		response = retry
	}
	return normalizeAssistantRole(response), nil
}

func normalizeAssistantRole(response model.Response) model.Response {
	if response.Message.Role == "" {
		response.Message.Role = model.RoleAssistant
	}
	return response
}

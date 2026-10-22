package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/prompt"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// PreparedContext is trusted profile-owned enrichment. It runs under session
// ownership and the turn budget. Additional values cannot replace recovered
// authorization or generation values; model output cannot provide this hook.
type PreparedContext struct {
	Prompt []prompt.Fragment
	Values map[string]string
}

func (r *Runtime) prepareContext(ctx context.Context, request TurnRequest) (TurnRequest, error) {
	if request.PrepareContext == nil {
		return request, nil
	}
	scope := request.Scope
	scope.Values = make(map[string]string, len(request.Scope.Values))
	for key, value := range request.Scope.Values {
		scope.Values[key] = value
	}
	prepared, err := request.PrepareContext(ctx, scope)
	if err != nil {
		return request, err
	}
	request.Prompt = append(append([]prompt.Fragment(nil), request.Prompt...), prepared.Prompt...)
	values := make(map[string]string, len(request.Scope.Values)+len(prepared.Values))
	for key, value := range prepared.Values {
		values[key] = value
	}
	for key, value := range request.Scope.Values {
		values[key] = value
	}
	request.Scope.Values = values
	return request, nil
}

func restorePreparedContext(request TurnRequest, events []transcript.Event) (TurnRequest, error) {
	for _, event := range events {
		if event.TurnID != request.TurnID || event.Type != transcript.TurnContextPrepared {
			continue
		}
		var prepared struct {
			Values map[string]string `json:"values"`
		}
		if err := json.Unmarshal(event.Metadata, &prepared); err != nil {
			return request, err
		}
		values := make(map[string]string, len(request.Scope.Values)+len(prepared.Values))
		for key, value := range request.Scope.Values {
			values[key] = value
		}
		for key, value := range prepared.Values {
			if original, exists := values[key]; exists && original != value {
				return request, fmt.Errorf("prepared context conflicts with original scope")
			}
			values[key] = value
		}
		request.Scope.Values = values
	}
	return request, nil
}

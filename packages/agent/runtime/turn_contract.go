package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// TurnLimits can tighten an operator's execution ceilings. Zero inherits the
// configured ceiling; a request cannot disable it. Deadlines remain owned by
// the invoking task/context, independently of the model-step limit.
type TurnLimits struct {
	MaxSteps       int   `json:"max_steps,omitempty"`
	MaxTotalTokens int64 `json:"max_total_tokens,omitempty"`
}

type executionBudget struct {
	MaxSteps       int             `json:"max_steps"`
	MaxTotalTokens int64           `json:"max_total_tokens"`
	Class          model.WorkClass `json:"class"`
}

func (r *Runtime) restoreTurnContract(ctx context.Context, request TurnRequest, events []transcript.Event) (TurnRequest, executionBudget, error) {
	if request.Limits.MaxSteps < 0 || request.Limits.MaxTotalTokens < 0 {
		return request, executionBudget{}, errors.New("turn limits must be non-negative; zero inherits the operator ceiling")
	}
	if request.WorkClass != "" && request.WorkClass != model.WorkInteractive && request.WorkClass != model.WorkBackground {
		return request, executionBudget{}, errors.New("invalid turn workload class")
	}
	budget := executionBudget{MaxSteps: r.config.MaxSteps, MaxTotalTokens: r.config.MaxTotalTokens, Class: model.WorkInteractive}
	if request.WorkClass == model.WorkBackground || request.Parent != nil {
		budget.Class = model.WorkBackground
	}
	budget.MaxSteps = tighterStepLimit(budget.MaxSteps, request.Limits.MaxSteps)
	budget.MaxTotalTokens = tighterTokenLimit(budget.MaxTotalTokens, request.Limits.MaxTotalTokens)
	for _, event := range events {
		if event.TurnID != request.TurnID || event.Type != transcript.TurnStarted || len(event.Metadata) == 0 {
			continue
		}
		var original struct {
			UserID    string            `json:"user_id"`
			Workspace string            `json:"workspace"`
			Scope     map[string]string `json:"scope"`
			Budget    *executionBudget  `json:"execution_budget"`
		}
		if err := json.Unmarshal(event.Metadata, &original); err != nil {
			return request, executionBudget{}, err
		}
		// Authorization and ceilings come from the same durable start receipt.
		// Preparing fresh context must not authorize an earlier suspended write.
		if original.UserID != "" && request.Scope.UserID != "" && original.UserID != request.Scope.UserID {
			return request, executionBudget{}, errors.New("turn belongs to a different user")
		}
		if original.Scope != nil {
			request.Scope.Values = original.Scope
		}
		if original.UserID != "" {
			request.Scope.UserID = original.UserID
		}
		if original.Workspace != "" {
			request.Scope.Workspace = original.Workspace
		}
		if original.Budget == nil {
			break
		} // Legacy starts did not capture ceilings.
		stored := original.Budget
		if (stored.MaxSteps != UnlimitedSteps && stored.MaxSteps <= 0) || stored.MaxTotalTokens < 0 || (stored.Class != model.WorkInteractive && stored.Class != model.WorkBackground) {
			return request, executionBudget{}, errors.New("invalid durable execution budget")
		}
		budget.MaxSteps = tighterStepLimit(budget.MaxSteps, stored.MaxSteps)
		budget.MaxTotalTokens = tighterTokenLimit(budget.MaxTotalTokens, stored.MaxTotalTokens)
		budget.Class = stored.Class
		if request.Parent != nil {
			budget.Class = model.WorkBackground
		}
		break
	}
	if inherited := model.TokenBudgetLimit(ctx); inherited > 0 {
		// There is one parent/child token ledger. Replacing it would reset shared
		// consumption; silently accepting a smaller child cap would not enforce it.
		if budget.MaxTotalTokens > 0 && inherited > budget.MaxTotalTokens {
			return request, executionBudget{}, errors.New("turn token ceiling conflicts with its inherited shared budget")
		}
		budget.MaxTotalTokens = inherited
	}
	if budget.MaxTotalTokens > 0 && r.config.MaxOutputTokens <= 0 {
		return request, executionBudget{}, errors.New("finite token budget requires a positive model output limit")
	}
	return request, budget, nil
}

func tighterStepLimit(current, proposed int) int {
	if proposed > 0 && (current == UnlimitedSteps || proposed < current) {
		return proposed
	}
	return current
}
func tighterTokenLimit(current, proposed int64) int64 {
	if proposed > 0 && (current == 0 || proposed < current) {
		return proposed
	}
	return current
}

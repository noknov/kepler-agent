package runtime

import (
	"encoding/json"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type turnProgress struct {
	Steps          int
	Usage          model.Usage
	EmbeddingUsage model.Usage
	Budget         model.BudgetState
}

// projectTurnProgress derives live and recovered accounting from committed facts.
// Budget snapshots include delegated and compactor work via their shared context.
func projectTurnProgress(events []transcript.Event, turnID string, config Config) (turnProgress, error) {
	var progress turnProgress
	hasBudget := false
	var legacySpent int64
	seenTools := make(map[string]bool)
	unresolved := make(map[string]bool)
	for _, event := range events {
		if event.TurnID != turnID {
			continue
		}
		switch event.Type {
		case transcript.StepStarted:
			var data struct {
				Step int `json:"step"`
			}
			if err := json.Unmarshal(event.Metadata, &data); err != nil || data.Step < 0 {
				return progress, fmt.Errorf("invalid durable step metadata at sequence %d", event.Sequence)
			}
			if data.Step > progress.Steps {
				progress.Steps = data.Step
			}
		case transcript.ModelRequestStarted:
			var data modelRequestState
			if json.Unmarshal(event.Metadata, &data) == nil && data.RequestID != "" {
				unresolved[modelRequestKey(data)] = true
			}
		case transcript.ModelCompleted:
			var data struct {
				Usage model.Usage `json:"usage"`
			}
			if err := json.Unmarshal(event.Metadata, &data); err != nil {
				return progress, err
			}
			addUsage(&progress.Usage, data.Usage)
			legacySpent += model.UsageTokens(data.Usage)
			var request modelRequestState
			if json.Unmarshal(event.Metadata, &request) == nil {
				delete(unresolved, modelRequestKey(request))
			}
		case transcript.ToolCallCompleted, transcript.ToolCallFailed:
			if event.ToolCall == nil || event.ToolResult == nil || seenTools[event.ToolCall.ID] {
				continue
			}
			seenTools[event.ToolCall.ID] = true
			if usage := toolResultUsage(*event.ToolResult); usage != nil {
				addUsage(&progress.Usage, *usage)
				legacySpent += model.UsageTokens(*usage)
			}
		case transcript.ModelUsageSettled:
			var receipt model.AttemptUsage
			if err := json.Unmarshal(event.Metadata, &receipt); err != nil {
				return progress, err
			}
			if receipt.Operation == "embedding" {
				addUsage(&progress.EmbeddingUsage, receipt.Usage)
			}
		case transcript.TokenBudgetUpdated:
			if err := json.Unmarshal(event.Metadata, &progress.Budget); err != nil {
				return progress, err
			}
			if progress.Budget.Spent < 0 || progress.Budget.Reserved < 0 {
				return progress, fmt.Errorf("invalid durable token budget at sequence %d", event.Sequence)
			}
			hasBudget = true
		}
	}
	if !hasBudget {
		// Legacy turns did not record reservations. Known usage remains chargeable;
		// an unknown provider outcome conservatively consumes a full request ceiling.
		progress.Budget.Spent = legacySpent
		if config.MaxTotalTokens > 0 {
			progress.Budget.Spent += int64(len(unresolved)) * int64(config.Context.MaxTokens+config.MaxOutputTokens)
		}
	}
	return progress, nil
}

func toolResultUsage(result tool.Result) *model.Usage {
	if result.Usage != nil {
		return result.Usage
	}
	if value, ok := result.Metadata["usage"]; ok {
		raw, err := json.Marshal(value)
		var usage model.Usage
		if err == nil && json.Unmarshal(raw, &usage) == nil {
			return &usage
		}
	}
	return nil
}

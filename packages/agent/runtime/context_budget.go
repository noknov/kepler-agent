package runtime

import "github.com/noknov/kepler-agent/packages/agent/model"

// inputBudget is shared by projection and final admission. Tool schemas, output
// and safety reserve belong to the same window as conversational history.
func (c ContextConfig) inputBudget(tools []model.ToolDefinition) (int, error) {
	available := c.MaxTokens - c.ReserveTokens - c.OutputTokens - c.counter().Tools(tools)
	if available <= 0 {
		return 0, &model.Error{Kind: model.ErrorBudgetExhausted, Message: "context budget exhausted by reserved, output, and tool tokens"}
	}
	return available, nil
}

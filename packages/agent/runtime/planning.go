package runtime

import "github.com/noknov/kepler-agent/packages/agent/model"

const planningInstruction = `

For a complex multi-step task, use update_plan before substantial work and update it when a meaningful phase completes or the next phase changes. Keep the plan concise, concrete, and truthful: at most one item may be in_progress. Do not use update_plan for simple one-step answers, and do not narrate routine tool calls through it.`

func appendSystemInstruction(system model.Message, instruction string) model.Message {
	if instruction == "" {
		return system
	}
	if len(system.Content) == 0 {
		return model.TextMessage(model.RoleSystem, instruction)
	}
	system.Content[0].Text += instruction
	return system
}

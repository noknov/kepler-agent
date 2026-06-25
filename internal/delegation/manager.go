package delegation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/prompts"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type Profile struct {
	Name         string
	SystemPrompt string
}

type Manager struct {
	client       llm.Client
	model        string
	thinking     string
	tools        ToolExecutor
	profiles     map[string]Profile
	policyPrompt string
}

type ToolExecutor interface {
	Specs() []llm.ToolSpec
	Execute(ctx context.Context, name string, args json.RawMessage, rt registry.Runtime) (registry.Result, error)
	CanRunInParallel(name string) bool
}

func NewManager(client llm.Client, model, thinking string) *Manager {
	return &Manager{
		client:   client,
		model:    model,
		thinking: thinking,
		profiles: map[string]Profile{
			"code": {
				Name:         "code",
				SystemPrompt: prompts.Delegate("code", ""),
			},
			"incident": {
				Name:         "incident",
				SystemPrompt: prompts.Delegate("incident", ""),
			},
		},
	}
}

func (m *Manager) SetTools(tools ToolExecutor) {
	m.tools = tools
}

func (m *Manager) SetPolicyPrompt(prompt string) {
	m.policyPrompt = prompt
}

func (m *Manager) RulesAndSkillsPrompt() string {
	return m.policyPrompt
}

func (m *Manager) Run(ctx context.Context, profileName, task, contextText string) (string, error) {
	profile, ok := m.profiles[profileName]
	if !ok {
		return "", fmt.Errorf("unknown delegate profile %q", profileName)
	}
	resp, err := m.client.Chat(ctx, llm.Request{
		Model:    m.model,
		Thinking: "", // disable thinking in sub-agents for speed
		Messages: []llm.Message{
			{Role: "system", Content: profile.SystemPrompt + m.RulesAndSkillsPrompt()},
			{Role: "user", Content: "Task:\n" + task + "\n\nContext:\n" + contextText},
		},
		MaxTokens:   4096,
		Temperature: 0.1,
	})
	if err != nil {
		return "", err
	}
	return resp.Message.Content, nil
}

func (m *Manager) ProfilesJSON() string {
	names := make([]string, 0, len(m.profiles))
	for name := range m.profiles {
		names = append(names, name)
	}
	data, _ := json.Marshal(names)
	return string(data)
}

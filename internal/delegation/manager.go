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

type ExploreProfile struct {
	MaxSteps     int
	MaxTokens    int
	Parallelism  int
	MaxWorkers   int
	AllowedTools map[string]bool
	SystemPrompt string
	FinalPrompt  string
}

type Manager struct {
	client       llm.Client
	streamClient llm.StreamClient
	model        string
	thinking     string
	tools        ToolExecutor
	profiles     map[string]Profile
	explore      ExploreProfile
	policyPrompt string

	secondaryClient       llm.Client
	secondaryStreamClient llm.StreamClient
	secondaryModel        string
}

type ToolExecutor interface {
	Specs() []llm.ToolSpec
	Execute(ctx context.Context, name string, args json.RawMessage, rt registry.Runtime) (registry.Result, error)
	CanRunInParallel(name string) bool
}

func NewManager(client llm.Client, model, thinking string) *Manager {
	m := &Manager{
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
		explore: DefaultExploreProfile(),
	}
	if sc, ok := client.(llm.StreamClient); ok {
		m.streamClient = sc
	}
	return m
}

func DefaultExploreProfile() ExploreProfile {
	return ExploreProfile{
		MaxSteps:    exploreMaxSteps,
		MaxTokens:   exploreMaxTokens,
		Parallelism: 10,
		MaxWorkers:  3,
		AllowedTools: map[string]bool{
			"code-search":       true,
			"code-read_file":    true,
			"code-symbols":      true,
			"code-definition":   true,
			"code-references":   true,
			"code-diagnostics":  true,
			"repo-search":       true,
			"repo-read_file":    true,
			"git-search_ref":    true,
			"git-read_file_ref": true,
			"rag-search":        true,
		},
		SystemPrompt: prompts.Delegate("explore", defaultExploreSystemPrompt()),
		FinalPrompt:  defaultExploreFinalReportPrompt(),
	}
}

func (m *Manager) SetExploreProfile(profile ExploreProfile) {
	if profile.MaxSteps <= 0 {
		profile.MaxSteps = exploreMaxSteps
	}
	if profile.MaxTokens <= 0 {
		profile.MaxTokens = exploreMaxTokens
	}
	if profile.Parallelism <= 0 {
		profile.Parallelism = 10
	}
	if profile.MaxWorkers <= 0 {
		profile.MaxWorkers = 3
	}
	if len(profile.AllowedTools) == 0 {
		profile.AllowedTools = DefaultExploreProfile().AllowedTools
	}
	if profile.SystemPrompt == "" {
		profile.SystemPrompt = DefaultExploreProfile().SystemPrompt
	}
	if profile.FinalPrompt == "" {
		profile.FinalPrompt = DefaultExploreProfile().FinalPrompt
	}
	m.explore = profile
}

func (m *Manager) SetSecondaryClient(client llm.Client, model string) {
	m.secondaryClient = client
	m.secondaryModel = model
	if sc, ok := client.(llm.StreamClient); ok {
		m.secondaryStreamClient = sc
	}
}

func (m *Manager) SetStreamClient(client llm.StreamClient) {
	m.streamClient = client
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

func (m *Manager) resolveSecondaryModel() string {
	if m.secondaryModel != "" {
		return m.secondaryModel
	}
	return m.model
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

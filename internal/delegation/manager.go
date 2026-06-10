package delegation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/prompts"
)

type Profile struct {
	Name         string
	SystemPrompt string
}

type Manager struct {
	client   llm.Client
	model    string
	thinking string
	profiles map[string]Profile
	rules    []string
}

func NewManager(client llm.Client, model, thinking string) *Manager {
	return &Manager{
		client:   client,
		model:    model,
		thinking: thinking,
		profiles: map[string]Profile{
			"code": {
				Name:         "code",
				SystemPrompt: prompts.Delegate("code", "You are a focused analysis delegate. You cannot run tools or read the repository. Use only the supplied Context. Quote evidence verbatim from Context when citing code or paths. If Context is insufficient, list unknowns and recommend which real tools the main agent should run (for example code-search, code-read_file, git-log). Never invent file contents, APIs, or command output."),
			},
			"incident": {
				Name:         "incident",
				SystemPrompt: prompts.Delegate("incident", "You are an incident triage delegate. You cannot run tools. Use only the supplied Context. Build hypotheses, evidence quoted from Context, impact, and next checks. Never invent logs, metrics, or deployments. Recommend concrete follow-up tools for the main agent when verification is needed."),
			},
		},
	}
}

func (m *Manager) LoadMarkdown(rulesDir, _ string) error {
	rules, err := loadDir(rulesDir)
	if err != nil {
		return err
	}
	m.rules = rules
	return nil
}

func (m *Manager) RulesAndSkillsPrompt() string {
	var b strings.Builder
	if len(m.rules) > 0 {
		b.WriteString("\n\nAdditional rules:\n")
		b.WriteString(strings.Join(m.rules, "\n\n---\n\n"))
	}
	return b.String()
}

func (m *Manager) Run(ctx context.Context, profileName, task, contextText string) (string, error) {
	profile, ok := m.profiles[profileName]
	if !ok {
		return "", fmt.Errorf("unknown delegate profile %q", profileName)
	}
	resp, err := m.client.Chat(ctx, llm.Request{
		Model:    m.model,
		Thinking: m.thinking,
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

func loadDir(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, "# "+e.Name()+"\n"+strings.TrimSpace(string(data)))
	}
	return out, nil
}

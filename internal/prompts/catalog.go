package prompts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const DefaultDir = ".prompts"

type Catalog struct {
	System          string                `json:"system,omitempty"`
	Delegates       map[string]string     `json:"delegates,omitempty"`
	AppMessages     map[string]string     `json:"app_messages,omitempty"`
	Tools           map[string]ToolPrompt `json:"tools,omitempty"`
	MemoryLabels    map[string]string     `json:"memory_labels,omitempty"`
	ToolStatuses    map[string]string     `json:"tool_statuses,omitempty"`
	GitHubWorkflows map[string]string     `json:"github_workflows,omitempty"`
}

type ToolPrompt struct {
	Description string            `json:"description,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
}

var current = struct {
	sync.RWMutex
	dir     string
	catalog Catalog
}{
	dir: DefaultDir,
}

func LoadFromEnv() {
	dir := strings.TrimSpace(os.Getenv("PROMPT_DIR"))
	if dir == "" {
		dir = DefaultDir
	}
	_ = LoadDir(dir)
}

func Dir() string {
	current.RLock()
	defer current.RUnlock()
	return current.dir
}

func LoadDir(dir string) error {
	catalog := Catalog{
		Delegates:       map[string]string{},
		AppMessages:     map[string]string{},
		Tools:           map[string]ToolPrompt{},
		MemoryLabels:    map[string]string{},
		ToolStatuses:    map[string]string{},
		GitHubWorkflows: map[string]string{},
	}
	readText(filepath.Join(dir, "system.md"), &catalog.System)
	readJSON(filepath.Join(dir, "delegates.json"), &catalog.Delegates)
	readJSON(filepath.Join(dir, "app_messages.json"), &catalog.AppMessages)
	readJSON(filepath.Join(dir, "tools.json"), &catalog.Tools)
	readJSON(filepath.Join(dir, "memory.json"), &catalog.MemoryLabels)
	readJSON(filepath.Join(dir, "tool_statuses.json"), &catalog.ToolStatuses)
	readJSON(filepath.Join(dir, "github_workflows.json"), &catalog.GitHubWorkflows)

	current.Lock()
	current.dir = dir
	current.catalog = catalog
	current.Unlock()
	return nil
}

func System(fallback string) string {
	current.RLock()
	defer current.RUnlock()
	return choose(current.catalog.System, fallback)
}

func Delegate(name, fallback string) string {
	current.RLock()
	defer current.RUnlock()
	return choose(current.catalog.Delegates[name], fallback)
}

func AppMessage(name, fallback string) string {
	current.RLock()
	defer current.RUnlock()
	return choose(current.catalog.AppMessages[name], fallback)
}

func ToolDescription(name, fallback string) string {
	current.RLock()
	defer current.RUnlock()
	return choose(current.catalog.Tools[name].Description, fallback)
}

func ParamDescription(tool, param, fallback string) string {
	current.RLock()
	defer current.RUnlock()
	return choose(current.catalog.Tools[tool].Parameters[param], fallback)
}

func MemoryLabel(name, fallback string) string {
	current.RLock()
	defer current.RUnlock()
	return choose(current.catalog.MemoryLabels[name], fallback)
}

func ToolStatus(name, fallback string) string {
	current.RLock()
	defer current.RUnlock()
	return choose(current.catalog.ToolStatuses[name], fallback)
}

func GitHubWorkflow(name, fallback string) string {
	current.RLock()
	defer current.RUnlock()
	return choose(current.catalog.GitHubWorkflows[name], fallback)
}

func choose(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func readText(path string, out *string) {
	data, err := os.ReadFile(path)
	if err == nil {
		*out = strings.TrimSpace(string(data))
	}
}

func readJSON[T any](path string, out *T) {
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, out)
	}
}

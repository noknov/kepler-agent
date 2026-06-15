package prompts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const DefaultDir = ".prompts"
const PublicDir = "prompts"

type Catalog struct {
	System          string                `json:"system,omitempty"`
	Delegates       map[string]string     `json:"delegates,omitempty"`
	AppMessages     map[string]string     `json:"app_messages,omitempty"`
	Tools           map[string]ToolPrompt `json:"tools,omitempty"`
	MemoryLabels    map[string]string     `json:"memory_labels,omitempty"`
	ToolStatuses    map[string]string     `json:"tool_statuses,omitempty"`
	GitHubWorkflows map[string]string     `json:"github_workflows,omitempty"`
	Runner          map[string]string     `json:"runner,omitempty"`
	Health          map[string]string     `json:"health,omitempty"`
	Texts           map[string]string     `json:"texts,omitempty"`
	Rules           []string              `json:"-"`
	Skills          []Skill               `json:"-"`
}

type ToolPrompt struct {
	Description string            `json:"description,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
}

type Skill struct {
	Name        string
	Description string
	Content     string
	Source      string
}

var current = struct {
	sync.RWMutex
	dir     string
	dirs    []string
	catalog Catalog
}{
	dir: DefaultDir,
}

func init() {
	_ = LoadDirs(PublicDir)
}

func LoadFromEnv() {
	dir := strings.TrimSpace(os.Getenv("PROMPT_DIR"))
	if dir == "" {
		dir = DefaultDir
	}
	_ = LoadDirs(PublicDir, dir)
}

func Dir() string {
	current.RLock()
	defer current.RUnlock()
	return current.dir
}

func Dirs() []string {
	current.RLock()
	defer current.RUnlock()
	return append([]string(nil), current.dirs...)
}

func LoadDir(dir string) error {
	return LoadDirs(dir)
}

func LoadDirs(dirs ...string) error {
	catalog := newCatalog()
	loaded := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		resolved := resolveDir(dir)
		next := newCatalog()
		loadCatalogDir(resolved, &next)
		mergeCatalog(&catalog, next)
		loaded = append(loaded, resolved)
	}
	activeDir := DefaultDir
	if len(loaded) > 0 {
		activeDir = loaded[len(loaded)-1]
	}

	current.Lock()
	current.dir = activeDir
	current.dirs = loaded
	current.catalog = catalog
	current.Unlock()
	return nil
}

func resolveDir(dir string) string {
	if filepath.IsAbs(dir) {
		return dir
	}
	if looksLikeCatalogDir(dir) {
		return dir
	}
	wd, err := os.Getwd()
	if err != nil {
		return dir
	}
	for {
		candidate := filepath.Join(wd, dir)
		if looksLikeCatalogDir(candidate) {
			return candidate
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			break
		}
		wd = parent
	}
	return dir
}

func looksLikeCatalogDir(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	markers := []string{
		"system.md",
		"delegates.json",
		"app_messages.json",
		"tools.json",
		"memory.json",
		"runner.json",
		"health.json",
		"texts.json",
		"rules",
		"skills",
		"runbooks",
	}
	for _, marker := range markers {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

func newCatalog() Catalog {
	return Catalog{
		Delegates:       map[string]string{},
		AppMessages:     map[string]string{},
		Tools:           map[string]ToolPrompt{},
		MemoryLabels:    map[string]string{},
		ToolStatuses:    map[string]string{},
		GitHubWorkflows: map[string]string{},
		Runner:          map[string]string{},
		Health:          map[string]string{},
		Texts:           map[string]string{},
	}
}

func loadCatalogDir(dir string, catalog *Catalog) {
	readText(filepath.Join(dir, "system.md"), &catalog.System)
	readJSON(filepath.Join(dir, "delegates.json"), &catalog.Delegates)
	readJSON(filepath.Join(dir, "app_messages.json"), &catalog.AppMessages)
	readJSON(filepath.Join(dir, "tools.json"), &catalog.Tools)
	readJSON(filepath.Join(dir, "memory.json"), &catalog.MemoryLabels)
	readJSON(filepath.Join(dir, "tool_statuses.json"), &catalog.ToolStatuses)
	readJSON(filepath.Join(dir, "github_workflows.json"), &catalog.GitHubWorkflows)
	readJSON(filepath.Join(dir, "runner.json"), &catalog.Runner)
	readJSON(filepath.Join(dir, "health.json"), &catalog.Health)
	readJSON(filepath.Join(dir, "texts.json"), &catalog.Texts)
	catalog.Rules = readMarkdownDir(filepath.Join(dir, "rules"))
	catalog.Skills = readSkillsDir(filepath.Join(dir, "skills"))
}

func mergeCatalog(dst *Catalog, src Catalog) {
	dst.System = choose(src.System, dst.System)
	mergeStringMap(dst.Delegates, src.Delegates)
	mergeStringMap(dst.AppMessages, src.AppMessages)
	mergeToolMap(dst.Tools, src.Tools)
	mergeStringMap(dst.MemoryLabels, src.MemoryLabels)
	mergeStringMap(dst.ToolStatuses, src.ToolStatuses)
	mergeStringMap(dst.GitHubWorkflows, src.GitHubWorkflows)
	mergeStringMap(dst.Runner, src.Runner)
	mergeStringMap(dst.Health, src.Health)
	mergeStringMap(dst.Texts, src.Texts)
	dst.Rules = append(dst.Rules, src.Rules...)
	dst.Skills = mergeSkills(dst.Skills, src.Skills)
}

func mergeStringMap(dst, src map[string]string) {
	for key, value := range src {
		if strings.TrimSpace(value) != "" {
			dst[key] = value
		}
	}
}

func mergeToolMap(dst, src map[string]ToolPrompt) {
	for name, incoming := range src {
		current := dst[name]
		current.Description = choose(incoming.Description, current.Description)
		if current.Parameters == nil {
			current.Parameters = map[string]string{}
		}
		mergeStringMap(current.Parameters, incoming.Parameters)
		dst[name] = current
	}
}

func mergeSkills(dst, src []Skill) []Skill {
	if len(src) == 0 {
		return dst
	}
	byName := map[string]int{}
	for i, skill := range dst {
		byName[strings.ToLower(skill.Name)] = i
	}
	for _, skill := range src {
		key := strings.ToLower(skill.Name)
		if i, ok := byName[key]; ok {
			dst[i] = skill
			continue
		}
		byName[key] = len(dst)
		dst = append(dst, skill)
	}
	return dst
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

func RunnerPrompt(name, fallback string) string {
	current.RLock()
	defer current.RUnlock()
	return choose(current.catalog.Runner[name], fallback)
}

func HealthPrompt(name, fallback string) string {
	current.RLock()
	defer current.RUnlock()
	return choose(current.catalog.Health[name], fallback)
}

func PromptText(name, fallback string) string {
	current.RLock()
	defer current.RUnlock()
	return choose(current.catalog.Texts[name], fallback)
}

func RulesPrompt() string {
	current.RLock()
	defer current.RUnlock()
	var b strings.Builder
	if len(current.catalog.Rules) > 0 {
		b.WriteString(choose(current.catalog.Texts["rules_header"], ""))
		b.WriteString(strings.Join(current.catalog.Rules, "\n\n---\n\n"))
	}
	return b.String()
}

func SkillsPrompt() string {
	current.RLock()
	defer current.RUnlock()
	if len(current.catalog.Skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(choose(current.catalog.Texts["skills_header"], ""))
	b.WriteString(choose(current.catalog.Texts["skills_loading_policy"], ""))
	for _, skill := range current.catalog.Skills {
		b.WriteString("\n- ")
		b.WriteString(skill.Name)
		if skill.Description != "" {
			b.WriteString(": ")
			b.WriteString(skill.Description)
		}
	}
	return b.String()
}

func RulesAndSkillsPrompt() string {
	return RulesPrompt() + SkillsPrompt()
}

func LoadSkill(name string) (Skill, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Skill{}, false
	}
	current.RLock()
	defer current.RUnlock()
	if len(current.catalog.Skills) > 0 {
		for _, skill := range current.catalog.Skills {
			if strings.EqualFold(skill.Name, name) {
				return skill, true
			}
		}
		for _, skill := range current.catalog.Skills {
			if strings.EqualFold(skill.Source, name) {
				return skill, true
			}
		}
	}
	return Skill{}, false
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

func readMarkdownDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		switch {
		case entry.IsDir():
			path := filepath.Join(dir, entry.Name(), "SKILL.md")
			if data, err := os.ReadFile(path); err == nil {
				out = append(out, "# "+entry.Name()+"/SKILL.md\n"+strings.TrimSpace(string(data)))
			}
		case filepath.Ext(entry.Name()) == ".md":
			path := filepath.Join(dir, entry.Name())
			if data, err := os.ReadFile(path); err == nil {
				out = append(out, "# "+entry.Name()+"\n"+strings.TrimSpace(string(data)))
			}
		}
	}
	return out
}

func readSkillsDir(dir string) []Skill {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return nil
	}
	var out []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		skill := parseSkill(entry.Name(), strings.TrimSpace(string(data)))
		skill.Source = entry.Name() + "/SKILL.md"
		out = append(out, skill)
	}
	return out
}

func parseSkill(fallbackName, content string) Skill {
	name := fallbackName
	description := ""
	if strings.HasPrefix(content, "---\n") {
		if end := strings.Index(content[len("---\n"):], "\n---"); end >= 0 {
			frontmatter := content[len("---\n") : len("---\n")+end]
			for _, line := range strings.Split(frontmatter, "\n") {
				key, value, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				value = strings.Trim(strings.TrimSpace(value), `"'`)
				switch strings.TrimSpace(key) {
				case "name":
					if value != "" {
						name = value
					}
				case "description":
					description = value
				}
			}
		}
	}
	return Skill{
		Name:        name,
		Description: description,
		Content:     content,
	}
}

func readJSON[T any](path string, out *T) {
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, out)
	}
}

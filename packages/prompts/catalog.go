package prompts

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const DefaultDir = "worker/.prompts"
const PublicDir = "packages/prompts/defaults"
const PrivateDir = "packages/prompts/private"

//go:embed defaults
var embeddedDefaults embed.FS

//go:embed all:private
var embeddedPrivate embed.FS

// DynamicBoundaryMarker separates cacheable static prompt content from runtime
// context so LLM providers can set prompt-cache breakpoints after the static portion.
const DynamicBoundaryMarker = "\n\n---DYNAMIC_CONTEXT_BELOW---\n\n"

var staticPromptCache struct {
	mu     sync.Mutex
	once   sync.Once
	prompt string
}

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
	RuleKeys        []string              `json:"-"`
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
	_ = LoadDirs(PublicDir, PrivateDir)
}

func LoadFromEnv() {
	dir := strings.TrimSpace(os.Getenv("PROMPT_DIR"))
	if dir == "" {
		_ = LoadDirs(PublicDir, PrivateDir)
		return
	}
	_ = LoadDirs(PublicDir, PrivateDir, dir)
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
		resolved := PublicDir
		if !isPublicDirRef(dir) {
			resolved = resolveDir(dir)
		}
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
	resetStaticPromptCache()
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

func isPublicDirRef(dir string) bool {
	dir = filepath.ToSlash(filepath.Clean(strings.TrimSpace(dir)))
	return dir == PublicDir || strings.HasSuffix(dir, "/"+PublicDir)
}

func isPrivateDirRef(dir string) bool {
	dir = filepath.ToSlash(filepath.Clean(strings.TrimSpace(dir)))
	return dir == PrivateDir || strings.HasSuffix(dir, "/"+PrivateDir)
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
	if isPublicDirRef(dir) {
		loadCatalogFS(embeddedDefaults, "defaults", catalog)
		return
	}
	if isPrivateDirRef(dir) {
		loadCatalogFS(embeddedPrivate, "private", catalog)
		return
	}
	loadCatalogFS(os.DirFS(dir), ".", catalog)
}

func loadCatalogFS(fsys fs.FS, root string, catalog *Catalog) {
	var systemPrompt string
	var agentAddendum string
	readTextFS(fsys, fsPath(root, "system.md"), &systemPrompt)
	readTextFS(fsys, fsPath(root, "agent.md"), &agentAddendum)
	catalog.System = appendPrompt(systemPrompt, agentAddendum)
	readJSONFS(fsys, fsPath(root, "delegates.json"), &catalog.Delegates)
	readJSONFS(fsys, fsPath(root, "app_messages.json"), &catalog.AppMessages)
	readJSONFS(fsys, fsPath(root, "tools.json"), &catalog.Tools)
	readJSONFS(fsys, fsPath(root, "memory.json"), &catalog.MemoryLabels)
	readJSONFS(fsys, fsPath(root, "tool_statuses.json"), &catalog.ToolStatuses)
	readJSONFS(fsys, fsPath(root, "github_workflows.json"), &catalog.GitHubWorkflows)
	readJSONFS(fsys, fsPath(root, "runner.json"), &catalog.Runner)
	readJSONFS(fsys, fsPath(root, "health.json"), &catalog.Health)
	readJSONFS(fsys, fsPath(root, "texts.json"), &catalog.Texts)
	readRuntimeJSONFS(fsys, fsPath(root, "runtime.json"), catalog)
	catalog.RuleKeys, catalog.Rules = readMarkdownDirFS(fsys, fsPath(root, "rules"))
	catalog.Skills = readSkillsDirFS(fsys, fsPath(root, "skills"))
}

func fsPath(root string, parts ...string) string {
	if root == "." {
		return pathJoin(parts...)
	}
	return pathJoin(append([]string{root}, parts...)...)
}

func pathJoin(parts ...string) string {
	return strings.Trim(strings.Join(parts, "/"), "/")
}

func mergeCatalog(dst *Catalog, src Catalog) {
	dst.System = appendPrompt(dst.System, src.System)
	mergeStringMap(dst.Delegates, src.Delegates)
	mergeStringMap(dst.AppMessages, src.AppMessages)
	mergeToolMap(dst.Tools, src.Tools)
	mergeStringMap(dst.MemoryLabels, src.MemoryLabels)
	mergeStringMap(dst.ToolStatuses, src.ToolStatuses)
	mergeStringMap(dst.GitHubWorkflows, src.GitHubWorkflows)
	mergeStringMap(dst.Runner, src.Runner)
	mergeStringMap(dst.Health, src.Health)
	mergeStringMap(dst.Texts, src.Texts)
	dst.RuleKeys, dst.Rules = mergeRules(dst.RuleKeys, dst.Rules, src.RuleKeys, src.Rules)
	dst.Skills = mergeSkills(dst.Skills, src.Skills)
}

func appendPrompt(base, addendum string) string {
	base = strings.TrimSpace(base)
	addendum = strings.TrimSpace(addendum)
	switch {
	case base == "":
		return addendum
	case addendum == "":
		return base
	default:
		return base + "\n\n" + addendum
	}
}

func readRuntimeJSONFS(fsys fs.FS, path string, catalog *Catalog) {
	var runtime struct {
		AppMessages     map[string]string `json:"app_messages,omitempty"`
		MemoryLabels    map[string]string `json:"memory_labels,omitempty"`
		ToolStatuses    map[string]string `json:"tool_statuses,omitempty"`
		GitHubWorkflows map[string]string `json:"github_workflows,omitempty"`
		Runner          map[string]string `json:"runner,omitempty"`
		Health          map[string]string `json:"health,omitempty"`
		Texts           map[string]string `json:"texts,omitempty"`
	}
	readJSONFS(fsys, path, &runtime)
	mergeStringMap(catalog.AppMessages, runtime.AppMessages)
	mergeStringMap(catalog.MemoryLabels, runtime.MemoryLabels)
	mergeStringMap(catalog.ToolStatuses, runtime.ToolStatuses)
	mergeStringMap(catalog.GitHubWorkflows, runtime.GitHubWorkflows)
	mergeStringMap(catalog.Runner, runtime.Runner)
	mergeStringMap(catalog.Health, runtime.Health)
	mergeStringMap(catalog.Texts, runtime.Texts)
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

func mergeRules(dstKeys, dstRules, srcKeys, srcRules []string) ([]string, []string) {
	if len(srcRules) == 0 {
		return dstKeys, dstRules
	}
	index := map[string]int{}
	for i, key := range dstKeys {
		index[key] = i
	}
	for i, rule := range srcRules {
		key := srcKeys[i]
		if existing, ok := index[key]; ok {
			dstRules[existing] = rule
			continue
		}
		index[key] = len(dstKeys)
		dstKeys = append(dstKeys, key)
		dstRules = append(dstRules, rule)
	}
	return dstKeys, dstRules
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

func resetStaticPromptCache() {
	staticPromptCache.mu.Lock()
	staticPromptCache.once = sync.Once{}
	staticPromptCache.prompt = ""
	staticPromptCache.mu.Unlock()
}

// StaticSystemPrompt returns the memoized static system prompt (system.md, agent.md,
// rules, and skill metadata). It is computed once per catalog load.
func StaticSystemPrompt() string {
	staticPromptCache.once.Do(func() {
		staticPromptCache.prompt = System("") + RulesAndSkillsPrompt()
	})
	staticPromptCache.mu.Lock()
	defer staticPromptCache.mu.Unlock()
	return staticPromptCache.prompt
}

// DynamicSystemPrompt returns the runtime-varying portion of the system prompt.
// When repoInventory is non-empty it is prefixed with DynamicBoundaryMarker.
func DynamicSystemPrompt(repoInventory string) string {
	repoInventory = strings.TrimSpace(repoInventory)
	if repoInventory == "" {
		return ""
	}
	return DynamicBoundaryMarker + PromptText("repository_inventory_header", "") + repoInventory
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

func readTextFS(fsys fs.FS, path string, out *string) {
	data, err := fs.ReadFile(fsys, path)
	if err == nil {
		*out = strings.TrimSpace(string(data))
	}
}

func readMarkdownDirFS(fsys fs.FS, dir string) ([]string, []string) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, nil
	}
	var keys []string
	var out []string
	for _, entry := range entries {
		switch {
		case entry.IsDir():
			path := fsPath(dir, entry.Name(), "SKILL.md")
			if data, err := fs.ReadFile(fsys, path); err == nil {
				keys = append(keys, entry.Name()+"/SKILL.md")
				out = append(out, "# "+entry.Name()+"/SKILL.md\n"+strings.TrimSpace(string(data)))
			}
		case filepath.Ext(entry.Name()) == ".md":
			path := fsPath(dir, entry.Name())
			if data, err := fs.ReadFile(fsys, path); err == nil {
				keys = append(keys, entry.Name())
				out = append(out, "# "+entry.Name()+"\n"+strings.TrimSpace(string(data)))
			}
		}
	}
	return keys, out
}

func readSkillsDirFS(fsys fs.FS, dir string) []Skill {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil
	}
	var out []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := fsPath(dir, entry.Name(), "SKILL.md")
		data, err := fs.ReadFile(fsys, path)
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
			lines := strings.Split(frontmatter, "\n")
			for i := 0; i < len(lines); i++ {
				line := lines[i]
				// Skip indented lines (YAML arrays, nested maps) and
				// bare list items that belong to a preceding key.
				if len(line) > 0 && (line[0] == ' ' || line[0] == '\t' || line[0] == '-') {
					continue
				}
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
					if value == "|" || value == ">" {
						block, next := readFrontmatterBlock(lines, i+1)
						i = next - 1
						description = block
					} else {
						description = value
					}
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

func readFrontmatterBlock(lines []string, start int) (string, int) {
	var out []string
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			return strings.TrimSpace(strings.Join(out, "\n")), i
		}
		out = append(out, strings.TrimSpace(line))
	}
	return strings.TrimSpace(strings.Join(out, "\n")), len(lines)
}

func readJSONFS[T any](fsys fs.FS, path string, out *T) {
	data, err := fs.ReadFile(fsys, path)
	if err == nil {
		_ = json.Unmarshal(data, out)
	}
}

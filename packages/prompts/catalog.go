package prompts

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/noknov/kepler-agent/packages/frontmatter"
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

type Catalog struct {
	System          string                `json:"system,omitempty"`
	AppMessages     map[string]string     `json:"app_messages,omitempty"`
	Tools           map[string]ToolPrompt `json:"tools,omitempty"`
	ToolStatuses    map[string]string     `json:"tool_statuses,omitempty"`
	GitHubWorkflows map[string]string     `json:"github_workflows,omitempty"`
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
	dir          string
	dirs         []string
	catalog      Catalog
	staticPrompt string
}{
	dir: DefaultDir,
}

func init() {
	if err := LoadDirs(PublicDir, PrivateDir); err != nil {
		panic(err)
	}
}

func LoadFromEnv() error {
	dir := strings.TrimSpace(os.Getenv("PROMPT_DIR"))
	if dir == "" {
		return LoadDirs(PublicDir, PrivateDir)
	}
	return LoadDirs(PublicDir, PrivateDir, dir)
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
		resolved := filepath.Clean(dir)
		next := newCatalog()
		if err := loadCatalogDir(resolved, &next); err != nil {
			return fmt.Errorf("load prompt catalog %q: %w", dir, err)
		}
		mergeCatalog(&catalog, next)
		loaded = append(loaded, resolved)
	}
	activeDir := DefaultDir
	if len(loaded) > 0 {
		activeDir = loaded[len(loaded)-1]
	}

	staticPrompt := choose(catalog.System, "") + rulesPrompt(catalog) + skillsPrompt(catalog)
	current.Lock()
	current.dir = activeDir
	current.dirs = loaded
	current.catalog = catalog
	current.staticPrompt = staticPrompt
	current.Unlock()
	return nil
}

func isPublicDirRef(dir string) bool { return filepath.ToSlash(filepath.Clean(dir)) == PublicDir }

func isPrivateDirRef(dir string) bool { return filepath.ToSlash(filepath.Clean(dir)) == PrivateDir }

func newCatalog() Catalog {
	return Catalog{
		AppMessages:     map[string]string{},
		Tools:           map[string]ToolPrompt{},
		ToolStatuses:    map[string]string{},
		GitHubWorkflows: map[string]string{},
		Health:          map[string]string{},
		Texts:           map[string]string{},
	}
}

func loadCatalogDir(dir string, catalog *Catalog) error {
	if isPublicDirRef(dir) {
		return loadCatalogFS(embeddedDefaults, "defaults", catalog)
	}
	if isPrivateDirRef(dir) {
		return loadCatalogFS(embeddedPrivate, "private", catalog)
	}
	return loadCatalogFS(os.DirFS(dir), ".", catalog)
}

func loadCatalogFS(fsys fs.FS, root string, catalog *Catalog) error {
	info, err := fs.Stat(fsys, root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("catalog root is not a directory")
	}
	var systemPrompt, agentAddendum string
	if err := readTextFS(fsys, fsPath(root, "system.md"), &systemPrompt); err != nil {
		return err
	}
	if err := readTextFS(fsys, fsPath(root, "agent.md"), &agentAddendum); err != nil {
		return err
	}
	catalog.System = appendPrompt(systemPrompt, agentAddendum)
	for _, section := range []struct {
		name   string
		target any
	}{
		{"app_messages.json", &catalog.AppMessages}, {"tools.json", &catalog.Tools},
		{"tool_statuses.json", &catalog.ToolStatuses}, {"github_workflows.json", &catalog.GitHubWorkflows},
		{"health.json", &catalog.Health}, {"texts.json", &catalog.Texts},
	} {
		if err := readJSONFS(fsys, fsPath(root, section.name), section.target); err != nil {
			return err
		}
	}
	if err := readRuntimeJSONFS(fsys, fsPath(root, "runtime.json"), catalog); err != nil {
		return err
	}
	catalog.RuleKeys, catalog.Rules, err = readMarkdownDirFS(fsys, fsPath(root, "rules"))
	if err != nil {
		return err
	}
	catalog.Skills, err = readSkillsDirFS(fsys, fsPath(root, "skills"))
	return err
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
	mergeStringMap(dst.AppMessages, src.AppMessages)
	mergeToolMap(dst.Tools, src.Tools)
	mergeStringMap(dst.ToolStatuses, src.ToolStatuses)
	mergeStringMap(dst.GitHubWorkflows, src.GitHubWorkflows)
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

func readRuntimeJSONFS(fsys fs.FS, path string, catalog *Catalog) error {
	var runtime struct {
		AppMessages     map[string]string `json:"app_messages,omitempty"`
		ToolStatuses    map[string]string `json:"tool_statuses,omitempty"`
		GitHubWorkflows map[string]string `json:"github_workflows,omitempty"`
		Health          map[string]string `json:"health,omitempty"`
		Texts           map[string]string `json:"texts,omitempty"`
	}
	if err := readJSONFS(fsys, path, &runtime); err != nil {
		return err
	}
	mergeStringMap(catalog.AppMessages, runtime.AppMessages)
	mergeStringMap(catalog.ToolStatuses, runtime.ToolStatuses)
	mergeStringMap(catalog.GitHubWorkflows, runtime.GitHubWorkflows)
	mergeStringMap(catalog.Health, runtime.Health)
	mergeStringMap(catalog.Texts, runtime.Texts)
	return nil
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
	return rulesPrompt(current.catalog)
}

func rulesPrompt(catalog Catalog) string {
	var b strings.Builder
	if len(catalog.Rules) > 0 {
		b.WriteString(choose(catalog.Texts["rules_header"], ""))
		b.WriteString(strings.Join(catalog.Rules, "\n\n---\n\n"))
	}
	return b.String()
}

func SkillsPrompt() string {
	current.RLock()
	defer current.RUnlock()
	return skillsPrompt(current.catalog)
}

func skillsPrompt(catalog Catalog) string {
	if len(catalog.Skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(choose(catalog.Texts["skills_header"], ""))
	b.WriteString(choose(catalog.Texts["skills_loading_policy"], ""))
	for _, skill := range catalog.Skills {
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
	current.RLock()
	defer current.RUnlock()
	return rulesPrompt(current.catalog) + skillsPrompt(current.catalog)
}

// StaticSystemPrompt returns the memoized static system prompt (system.md, agent.md,
// rules, and skill metadata). It is computed once per catalog load.
func StaticSystemPrompt() string {
	current.RLock()
	defer current.RUnlock()
	return current.staticPrompt
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

// Missing sections are optional; errors reading present sections are not.
func readOptionalFileFS(fsys fs.FS, path string) ([]byte, bool, error) {
	data, err := fs.ReadFile(fsys, path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %q: %w", path, err)
	}
	return data, true, nil
}

func readTextFS(fsys fs.FS, path string, out *string) error {
	data, found, err := readOptionalFileFS(fsys, path)
	if err != nil {
		return err
	}
	if found {
		*out = strings.TrimSpace(string(data))
	}
	return nil
}

func readMarkdownDirFS(fsys fs.FS, dir string) ([]string, []string, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read %q: %w", dir, err)
	}
	var keys, out []string
	for _, entry := range entries {
		key := entry.Name()
		if entry.IsDir() {
			key += "/SKILL.md"
		} else if filepath.Ext(key) != ".md" {
			continue
		}
		data, found, err := readOptionalFileFS(fsys, fsPath(dir, key))
		if err != nil {
			return nil, nil, err
		}
		if found {
			keys = append(keys, key)
			out = append(out, "# "+key+"\n"+strings.TrimSpace(string(data)))
		}
	}
	return keys, out, nil
}

func readSkillsDirFS(fsys fs.FS, dir string) ([]Skill, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", dir, err)
	}
	var out []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := fsPath(dir, entry.Name(), "SKILL.md")
		data, found, err := readOptionalFileFS(fsys, path)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		skill, err := ParseSkill(entry.Name(), strings.TrimSpace(string(data)))
		if err != nil {
			return nil, fmt.Errorf("parse %q: %w", path, err)
		}
		skill.Source = entry.Name() + "/SKILL.md"
		out = append(out, skill)
	}
	return out, nil
}

// ParseSkill reads optional YAML metadata. Plain Markdown uses fallbackName;
// malformed metadata is an error rather than an unnamed or partially read skill.
func ParseSkill(fallbackName, content string) (Skill, error) {
	var header struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if _, _, err := frontmatter.Decode(content, &header); err != nil {
		return Skill{}, err
	}
	name := strings.TrimSpace(header.Name)
	if name == "" {
		name = fallbackName
	}
	return Skill{Name: name, Description: strings.TrimSpace(header.Description), Content: content}, nil
}

func readJSONFS(fsys fs.FS, path string, out any) error {
	data, found, err := readOptionalFileFS(fsys, path)
	if err != nil || !found {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode %q: %w", path, err)
	}
	return nil
}

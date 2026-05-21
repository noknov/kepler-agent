package safety

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wati/oncall-agent/internal/prompts"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)xox[baprs]-[a-z0-9-]+`),
	regexp.MustCompile(`(?i)(moonshot|openai|slack|notion|youtrack|github)[_\- ]?(api)?[_\- ]?(key|token|secret)\s*[:=]\s*['"]?[^'"\s]+`),
	regexp.MustCompile(`(?i)authorization:\s*bearer\s+[a-z0-9._\-]+`),
	regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]+?-----END [A-Z ]*PRIVATE KEY-----`),
}

type PromptPolicy struct {
	WorkspaceRoots []string
}

func (p PromptPolicy) SystemPrompt() string {
	base := prompts.System("You are an on-call debugging assistant running inside Slack. Follow the locally configured prompt files, protect secrets, use tools only for the user's task, and keep responses concise.")
	repos := p.discoverRepos()
	if repos != "" {
		base += "\n\nAvailable code repositories (use repo name as path prefix for tools, e.g. \"whatsapp_inbox/netcore-mvc/Startup.cs\"):\n" + repos
	}
	return base
}

func (p PromptPolicy) discoverRepos() string {
	var lines []string
	for _, root := range p.WorkspaceRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(root, e.Name())
			if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
				continue
			}
			stack := detectStack(dir)
			lines = append(lines, "- "+e.Name()+"/ ("+stack+")")
		}
	}
	return strings.Join(lines, "\n")
}

func detectStack(dir string) string {
	markers := []struct {
		file  string
		stack string
	}{
		{"go.mod", "Go"},
		{"package.json", "Node.js/TypeScript"},
		{"pom.xml", "Java/Maven"},
		{"build.gradle", "Java/Gradle"},
		{"requirements.txt", "Python"},
		{"Cargo.toml", "Rust"},
	}
	for _, m := range markers {
		if _, err := os.Stat(filepath.Join(dir, m.file)); err == nil {
			return m.stack
		}
	}
	// Check subdirectories for .sln or .csproj (C#/.NET)
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if strings.HasSuffix(name, ".sln") || strings.HasSuffix(name, ".csproj") {
				return "C#/.NET"
			}
			if e.IsDir() {
				sub := filepath.Join(dir, name)
				subEntries, _ := os.ReadDir(sub)
				for _, se := range subEntries {
					sn := se.Name()
					if strings.HasSuffix(sn, ".sln") || strings.HasSuffix(sn, ".csproj") {
						return "C#/.NET"
					}
				}
			}
		}
	}
	return "unknown stack"
}

func (PromptPolicy) CleanUserText(botUserID, text string) string {
	text = strings.ReplaceAll(text, "<@"+botUserID+">", "")
	return strings.TrimSpace(text)
}

type Redactor struct {
	WorkspaceRoots []string
}

func (r Redactor) Sanitize(text string) string {
	clean := text
	for _, re := range secretPatterns {
		clean = re.ReplaceAllString(clean, "[redacted]")
	}
	// Strip absolute workspace root prefixes so users never see local paths
	for _, root := range r.WorkspaceRoots {
		withSlash := root + "/"
		clean = strings.ReplaceAll(clean, withSlash, "")
	}
	return clean
}

type WorkspacePolicy struct {
	Roots []string
}

func (g WorkspacePolicy) Resolve(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}

	clean := filepath.Clean(path)

	if filepath.IsAbs(clean) {
		for _, root := range g.Roots {
			if isWithin(clean, filepath.Clean(root)) {
				return clean, nil
			}
		}
		return "", fmt.Errorf("path is outside allowed workspace roots")
	}

	// Relative path: try each root as a base
	for _, root := range g.Roots {
		candidate := filepath.Join(root, clean)
		if isWithin(candidate, filepath.Clean(root)) {
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("file not found in any workspace root: %s", clean)
}

func isWithin(path, root string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

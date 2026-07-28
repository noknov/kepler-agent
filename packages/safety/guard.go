package safety

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)xox[baprs]-[a-z0-9-]+`),
	regexp.MustCompile(`(?i)(moonshot|openai|slack|notion|youtrack|github)[_\- ]?(api)?[_\- ]?(key|token|secret)\s*[:=]\s*['"]?[^'"\s]+`),
	regexp.MustCompile(`(?i)authorization:\s*bearer\s+[a-z0-9._\-]+`),
	regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]+?-----END [A-Z ]*PRIVATE KEY-----`),
}

type PromptPolicy struct {
	WorkspaceRoots             []string
	IncludeRepositoryInventory bool
	Now                        func() time.Time
	Redis                      *redisclient.Client
}

const repoInventoryTTL = 5 * time.Minute

func (p PromptPolicy) SystemPrompt() string {
	base := prompts.StaticSystemPrompt()
	base += p.runtimeDatePrompt()
	if p.IncludeRepositoryInventory {
		base += prompts.DynamicSystemPrompt(p.cachedRepoInventory())
	}
	return base
}

func (p PromptPolicy) cachedRepoInventory() string {
	const key = "prompt:repo_inventory"
	if p.Redis != nil {
		if cached, err := p.Redis.Get(context.Background(), key); err == nil && cached != "" {
			return cached
		}
	}
	result := p.discoverRepos()
	if p.Redis != nil && result != "" {
		_ = p.Redis.Set(context.Background(), key, result, repoInventoryTTL)
	}
	return result
}

func (p PromptPolicy) runtimeDatePrompt() string {
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	if now.IsZero() {
		now = time.Now()
	}
	zone := strings.TrimSpace(now.Location().String())
	if zone == "" {
		zone = "Local"
	}
	return fmt.Sprintf("\n\nRuntime context:\n- Current date: %s\n- Current year: %d\n- Timezone: %s\n- Resolve relative date phrases such as today, yesterday, tomorrow, this year, current year, 今年, 本年, 今年高考, and latest against this runtime date. For current-year web searches, include %d in the search query unless the user explicitly asks for a different year.", now.Format("2006-01-02"), now.Year(), zone, now.Year())
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

func (g WorkspacePolicy) ResolveReadableFile(path string) (string, error) {
	resolved, err := g.Resolve(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory")
	}
	if IsSensitivePath(resolved) {
		return "", fmt.Errorf("refusing to read sensitive file %q", filepath.Base(resolved))
	}
	realPath, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	for _, root := range g.Roots {
		realRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
		if err != nil {
			realRoot = filepath.Clean(root)
		}
		if isWithin(realPath, realRoot) {
			return realPath, nil
		}
	}
	return "", fmt.Errorf("path resolves outside allowed workspace roots")
}

// IsSensitivePath prevents the agent from reading files that contain secrets.
// The agent runs on a shared server — any content it reads could be surfaced
// to users via Slack. Credentials, keys, and env files must stay protected.
func IsSensitivePath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	if base == "id_rsa" || base == "id_ed25519" || base == "credentials" || base == "credentials.json" {
		return true
	}
	sensitiveSuffixes := []string{".pem", ".key", ".p12", ".pfx", ".kubeconfig"}
	for _, suffix := range sensitiveSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	sensitiveParts := []string{"/.aws/", "/.gcloud/", "/.kube/", "/secrets/", "/credentials/"}
	normalized := "/" + strings.ToLower(filepath.ToSlash(path))
	for _, part := range sensitiveParts {
		if strings.Contains(normalized, part) {
			return true
		}
	}
	return false
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

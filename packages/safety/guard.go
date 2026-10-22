package safety

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/noknov/kepler-agent/packages/prompts"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)xox[baprs]-[a-z0-9-]+`),
	regexp.MustCompile(`(?i)\bsk-(?:ant-|proj-)?[a-z0-9_-]{16,}\b`),
	regexp.MustCompile(`(?i)\b(?:gh[pousr]_[a-z0-9_]{20,}|github_pat_[a-z0-9_]{20,})\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`(?i)https://hooks\.slack\.com/services/[a-z0-9/_-]+`),
	regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|redis|mongodb(?:\+srv)?):\/\/[^:\s\/]+:[^@\s]+@`),
	regexp.MustCompile(`(?i)\b[a-z0-9_-]*(?:api[_\- ]?)?(?:key|token|secret|password)\s*[:=]\s*['"]?[^'"\s,]+`),
	regexp.MustCompile(`(?i)authorization:\s*bearer\s+[a-z0-9._\-]+`),
	regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]+?-----END [A-Z ]*PRIVATE KEY-----`),
}

type PromptPolicy struct{}

func (PromptPolicy) SystemPrompt() string {
	return prompts.StaticSystemPrompt()
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
	resolved, err := g.ResolveReadablePath(path)
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
	return resolved, nil
}

// ResolveReadablePath resolves an existing file or directory through symlinks
// and verifies that the final path remains inside an allowed root.
func (g WorkspacePolicy) ResolveReadablePath(path string) (string, error) {
	resolved, err := g.Resolve(path)
	if err != nil {
		return "", err
	}
	if IsSensitivePath(resolved) {
		return "", fmt.Errorf("refusing to read sensitive path %q", filepath.Base(resolved))
	}
	realPath, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	if IsSensitivePath(realPath) {
		return "", fmt.Errorf("refusing to read sensitive path %q", filepath.Base(realPath))
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

// One policy vocabulary drives both filesystem reads and committed-tree
// pathspecs. Git exclusions are case-insensitive just like IsSensitivePath.
var sensitiveFileNames = []string{"id_rsa", "id_ed25519", "credentials", "credentials.json", ".netrc", ".npmrc", ".pypirc"}
var sensitiveFileSuffixes = []string{".pem", ".key", ".p12", ".pfx", ".kubeconfig"}
var sensitiveDirectories = []string{".aws", ".gcloud", ".kube", "secrets", "credentials"}
var sensitiveRelativeFiles = []string{".git/config", ".git/credentials", ".docker/config.json", ".config/gh/hosts.yml"}

// IsSensitivePath protects known credential locations. It is defense in depth,
// not a guarantee that arbitrary files cannot contain confidential data.
func IsSensitivePath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	for _, name := range sensitiveFileNames {
		if base == name {
			return true
		}
	}
	for _, suffix := range sensitiveFileSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	normalized := "/" + strings.Trim(strings.ToLower(filepath.ToSlash(path)), "/") + "/"
	for _, relative := range sensitiveRelativeFiles {
		if strings.HasSuffix(normalized, "/"+relative+"/") {
			return true
		}
	}
	for _, directory := range sensitiveDirectories {
		if strings.Contains(normalized, "/"+directory+"/") {
			return true
		}
	}
	return false
}

// SensitiveGitPathspecs applies the same readable-path policy to committed trees.
func SensitiveGitPathspecs() []string {
	patterns := []string{".env", ".env.*"}
	patterns = append(patterns, sensitiveFileNames...)
	for _, suffix := range sensitiveFileSuffixes {
		patterns = append(patterns, "*"+suffix)
	}
	for _, directory := range sensitiveDirectories {
		patterns = append(patterns, directory+"/**")
	}
	patterns = append(patterns, sensitiveRelativeFiles...)
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		out = append(out, ":(exclude,glob,icase)**/"+pattern)
	}
	return out
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

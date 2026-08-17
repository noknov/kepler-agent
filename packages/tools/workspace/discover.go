package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Repository struct {
	Name  string
	Stack string
}

func DiscoverRepositories(roots []string) []Repository {
	seen := make(map[string]Repository)
	for _, root := range normalizedRoots(roots) {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(root, entry.Name())
			if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
				continue
			}
			name := entry.Name() + "/"
			seen[name] = Repository{Name: name, Stack: detectStack(dir)}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	repos := make([]Repository, 0, len(names))
	for _, name := range names {
		repos = append(repos, seen[name])
	}
	return repos
}

func normalizedRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
		out = append(out, filepath.Clean(root))
	}
	return out
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
	for _, marker := range markers {
		if _, err := os.Stat(filepath.Join(dir, marker.file)); err == nil {
			return marker.stack
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "unknown stack"
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".sln") || strings.HasSuffix(name, ".csproj") {
			return "C#/.NET"
		}
		if entry.IsDir() {
			subEntries, _ := os.ReadDir(filepath.Join(dir, name))
			for _, subEntry := range subEntries {
				subName := subEntry.Name()
				if strings.HasSuffix(subName, ".sln") || strings.HasSuffix(subName, ".csproj") {
					return "C#/.NET"
				}
			}
		}
	}
	return "unknown stack"
}

package local

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Workspace struct {
	Root string
	Temp string
}

func NewWorkspace(root string) (Workspace, error) {
	if root == "" {
		return Workspace{}, fmt.Errorf("workspace root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Workspace{}, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Workspace{}, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return Workspace{}, err
	}
	if !info.IsDir() {
		return Workspace{}, fmt.Errorf("workspace root is not a directory")
	}
	temp, err := os.MkdirTemp("", "kepler-agent-*")
	if err != nil {
		return Workspace{}, err
	}
	return Workspace{Root: filepath.Clean(real), Temp: temp}, nil
}

func (w Workspace) Close() error {
	if w.Temp == "" {
		return nil
	}
	return os.RemoveAll(w.Temp)
}

func (w Workspace) Resolve(path string, forWrite bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(w.Root, path)
	}
	path = filepath.Clean(path)
	check := path
	if forWrite {
		for {
			if _, err := os.Lstat(check); err == nil {
				break
			}
			parent := filepath.Dir(check)
			if parent == check {
				break
			}
			check = parent
		}
	}
	if real, err := filepath.EvalSymlinks(check); err == nil {
		if check != path {
			path = filepath.Join(real, strings.TrimPrefix(path, check+string(filepath.Separator)))
		} else {
			path = real
		}
	}
	if !within(path, w.Root) {
		return "", fmt.Errorf("path is outside the workspace")
	}
	if sensitivePath(path) {
		return "", fmt.Errorf("refusing to access sensitive path %q", filepath.Base(path))
	}
	return path, nil
}

func within(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func sensitivePath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	if base == "id_rsa" || base == "id_ed25519" || base == "credentials.json" {
		return true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".kubeconfig"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	normalized := "/" + strings.ToLower(filepath.ToSlash(path))
	for _, suffix := range []string{"/.git/config", "/.git/credentials", "/.netrc", "/.npmrc", "/.pypirc", "/.docker/config.json", "/.config/gh/hosts.yml"} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	for _, part := range []string{"/.aws/", "/.gcloud/", "/.kube/", "/secrets/", "/credentials/"} {
		if strings.Contains(normalized, part) {
			return true
		}
	}
	return false
}

func (w Workspace) SensitivePaths() ([]string, error) {
	paths := make([]string, 0, 16)
	seen := make(map[string]bool)
	for _, relative := range []string{
		".git/config", ".git/credentials", ".netrc", ".npmrc", ".pypirc",
		".docker/config.json", ".config/gh/hosts.yml",
	} {
		path := filepath.Join(w.Root, filepath.FromSlash(relative))
		if _, err := os.Lstat(path); err == nil {
			paths = append(paths, path)
			seen[path] = true
		}
	}
	err := filepath.WalkDir(w.Root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == w.Root {
			return nil
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if sensitivePath(path) && !seen[path] {
			paths = append(paths, path)
			seen[path] = true
			if entry.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	return paths, err
}

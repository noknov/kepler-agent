package local

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/noknov/kepler-agent/packages/safety"
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
	realTemp, err := filepath.EvalSymlinks(temp)
	if err != nil {
		_ = os.RemoveAll(temp)
		return Workspace{}, err
	}
	return Workspace{Root: filepath.Clean(real), Temp: filepath.Clean(realTemp)}, nil
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
	if safety.IsSensitivePath(path) {
		return "", fmt.Errorf("refusing to access sensitive path %q", filepath.Base(path))
	}
	return path, nil
}

func within(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
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
		if safety.IsSensitivePath(path) && !seen[path] {
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

// OpenReadFile opens a regular workspace file without waiting for a pipe writer.
// The caller owns the returned file; the temporary root capability closes here.
func (w Workspace) OpenReadFile(path string) (*os.File, error) {
	resolved, err := w.Resolve(path, false)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(w.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, err := filepath.Rel(w.Root, resolved)
	if err != nil {
		return nil, err
	}
	return safety.OpenRegularFile(root, name)
}

// OpenWritableFile keeps local path interpretation at the profile boundary and
// delegates the confined mutation lifetime to the shared workspace policy.
func (w Workspace) OpenWritableFile(path string) (*safety.WorkspaceFile, error) {
	resolved, err := w.Resolve(path, true)
	if err != nil {
		return nil, err
	}
	return (safety.WorkspacePolicy{Roots: []string{w.Root}}).OpenWritableFile(resolved)
}

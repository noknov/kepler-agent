package safety

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceFile keeps file operations relative to an opened directory capability.
// Path checks enforce sensitive-file policy; os.Root confines the actual I/O even
// if a symlink changes after validation. Atomic replacement avoids mutating other
// hard links and leaves the previous file intact until the write commits.
type WorkspaceFile struct {
	root *os.Root
	name string
	path string
}

// Close releases the directory capability; callers must close each opened file.
func (f *WorkspaceFile) Close() error { return f.root.Close() }

// Path reports the selected path under its configured workspace root.
func (f *WorkspaceFile) Path() string { return f.path }

// OpenWritableFile resolves the nearest existing ancestor against workspace and
// sensitive-path policy and returns an owner for confined reads and mutations.
func (policy WorkspacePolicy) OpenWritableFile(raw string) (*WorkspaceFile, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("path is required")
	}
	clean := filepath.Clean(raw)
	for _, root := range policy.Roots {
		root = filepath.Clean(root)
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			// An explicit root prefix cannot be reinterpreted as a nested
			// path in another workspace when its selected root is unavailable.
			if !filepath.IsAbs(clean) && (clean == filepath.Base(root) || strings.HasPrefix(clean, filepath.Base(root)+string(filepath.Separator))) {
				return nil, err
			}
			continue
		}
		candidate := clean
		if !filepath.IsAbs(clean) {
			relative := strings.TrimPrefix(clean, filepath.Base(root)+string(filepath.Separator))
			if clean == filepath.Base(root) {
				relative = "."
			}
			candidate = filepath.Join(root, relative)
		}
		rel, err := filepath.Rel(root, candidate)
		if err != nil || !filepath.IsLocal(rel) {
			// Existing-file reads may already have canonicalized a configured
			// root alias (for example /var -> /private/var on macOS).
			rel, err = filepath.Rel(realRoot, candidate)
			if err != nil || !filepath.IsLocal(rel) {
				continue
			}
			candidate = filepath.Join(root, rel)
		}
		if IsSensitivePath(candidate) {
			return nil, fmt.Errorf("refusing to edit sensitive file %q", filepath.Base(candidate))
		}
		// Resolve the nearest existing ancestor through the same policy as reads.
		// Missing descendants can then be created without treating a link to an
		// external or sensitive target as an ordinary workspace directory.
		ancestor := candidate
		for {
			_, err = os.Lstat(ancestor)
			if err == nil {
				break
			}
			if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			parent := filepath.Dir(ancestor)
			if parent == ancestor {
				return nil, err
			}
			ancestor = parent
		}
		resolved, err := policy.ResolveReadablePath(ancestor)
		if err != nil {
			return nil, err
		}
		suffix, err := filepath.Rel(ancestor, candidate)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(resolved, suffix)
		if IsSensitivePath(target) {
			return nil, fmt.Errorf("refusing to edit sensitive file %q", filepath.Base(target))
		}
		for _, allowed := range policy.Roots {
			realAllowed, err := filepath.EvalSymlinks(allowed)
			if err != nil {
				continue
			}
			name, err := filepath.Rel(realAllowed, target)
			if err != nil || !filepath.IsLocal(name) {
				continue
			}
			opened, err := os.OpenRoot(realAllowed)
			if err != nil {
				return nil, err
			}
			return &WorkspaceFile{root: opened, name: name, path: filepath.Join(allowed, name)}, nil
		}
		return nil, fmt.Errorf("path resolves outside allowed workspace roots")
	}
	return nil, fmt.Errorf("path is outside allowed workspace roots")
}

const maxEditableBytes = 16 << 20

// Read returns a regular file, bounded to the 16 MiB exact-edit budget.
func (f *WorkspaceFile) Read() ([]byte, error) {
	file, err := OpenRegularFile(f.root, f.name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxEditableBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxEditableBytes {
		return nil, fmt.Errorf("file exceeds %d byte edit limit", maxEditableBytes)
	}
	return data, nil
}

// Write replaces a file atomically, retaining existing permissions. New files
// use createMode and new directories are private to the workspace owner.
func (f *WorkspaceFile) Write(ctx context.Context, data []byte, createMode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mode := createMode.Perm()
	if info, err := f.root.Stat(f.name); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("write target is not a regular file")
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := f.root.MkdirAll(filepath.Dir(f.name), 0o700); err != nil {
		return err
	}
	parent, err := f.root.OpenRoot(filepath.Dir(f.name))
	if err != nil {
		return err
	}
	defer parent.Close()
	temporary := ".agent-write-" + rand.Text()
	file, err := parent.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
		_ = parent.Remove(temporary)
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return parent.Rename(temporary, filepath.Base(f.name))
}

// OpenRegularFile acquires a regular file relative to root without waiting for a
// FIFO writer on Unix. The caller owns the returned file, independently of root.
func OpenRegularFile(root *os.Root, name string) (*os.File, error) {
	file, err := root.OpenFile(name, workspaceReadFlags, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("read target is not a regular file")
	}
	return file, nil
}

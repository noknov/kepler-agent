package local

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
)

// HOME is caller-controlled (including in benchmark containers). Protect both
// the configured home and the OS account home before granting owned paths.
func sandboxPrivateRoots() (homes, privateRoots []string, err error) {
	account, err := user.Current()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve sandbox account: %w", err)
	}
	configuredHome, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve sandbox home: %w", err)
	}
	for _, home := range []string{account.HomeDir, configuredHome} {
		real, err := canonicalPrivateRoot(home)
		if err != nil {
			return nil, nil, err
		}
		homes = append(homes, real)
	}
	roots := append(append([]string(nil), homes...), os.TempDir(), "/tmp", "/var/tmp")
	for i, root := range roots {
		roots[i], err = canonicalPrivateRoot(root)
		if err != nil {
			return nil, nil, err
		}
	}
	// Parents sort before descendants. Hiding a parent already hides its children;
	// nested tmpfs mounts would otherwise overwrite newly recreated bind parents.
	sort.Strings(roots)
	for _, root := range roots {
		covered := false
		for _, parent := range privateRoots {
			covered = covered || within(root, parent)
		}
		if !covered {
			privateRoots = append(privateRoots, root)
		}
	}
	return homes, privateRoots, nil
}

func canonicalPrivateRoot(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return "", fmt.Errorf("sandbox private root must be an absolute non-root directory")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox private root: %w", err)
	}
	if real == string(filepath.Separator) {
		return "", fmt.Errorf("sandbox private root resolves to filesystem root")
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("sandbox private root must be an existing directory: %s", real)
	}
	return real, nil
}

package tool

import "strings"

// NormalizePaths merges a single path with a path list, preserving order and
// dropping blanks and duplicates. Empty input yields a nil slice.
func NormalizePaths(path string, paths []string) []string {
	seen := make(map[string]bool, 1+len(paths))
	out := make([]string, 0, 1+len(paths))
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}
	add(path)
	for _, value := range paths {
		add(value)
	}
	return out
}

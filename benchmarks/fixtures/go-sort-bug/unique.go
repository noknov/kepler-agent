package unique

// StableUnique removes duplicate strings while retaining first-seen order.
func StableUnique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			out = append(out, value)
		}
		seen[value] = true
	}
	return out
}

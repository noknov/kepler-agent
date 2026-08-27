package frontmatter

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Decode parses a leading YAML front matter document. It accepts LF or CRLF
// input and leaves content without front matter untouched.
func Decode(content string, target any) (body string, found bool, err error) {
	normalized := strings.TrimPrefix(strings.ReplaceAll(content, "\r\n", "\n"), "\ufeff")
	if !strings.HasPrefix(normalized, "---\n") {
		return content, false, nil
	}
	rest := normalized[4:]
	end := strings.Index(rest, "\n---\n")
	terminatorSize := len("\n---\n")
	if end < 0 && strings.HasSuffix(rest, "\n---") {
		end = len(rest) - len("\n---")
		terminatorSize = len("\n---")
	}
	if end < 0 {
		return content, false, fmt.Errorf("unterminated YAML front matter")
	}
	header := []byte(rest[:end])
	if target != nil && len(bytes.TrimSpace(header)) > 0 {
		if err := yaml.Unmarshal(header, target); err != nil {
			return content, true, fmt.Errorf("decode YAML front matter: %w", err)
		}
	}
	return rest[end+terminatorSize:], true, nil
}

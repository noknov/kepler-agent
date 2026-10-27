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
	end, bodyStart := -1, 0
	for offset := 0; offset <= len(rest); {
		line, _, newline := strings.Cut(rest[offset:], "\n")
		if line == "---" {
			end = offset
			bodyStart = offset + len(line)
			if newline {
				bodyStart++
			}
			break
		}
		if !newline {
			break
		}
		offset += len(line) + 1
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
	return rest[bodyStart:], true, nil
}

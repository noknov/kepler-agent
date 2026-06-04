package slack

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// DefaultMaxTextExtractChars limits how much text-file content is injected into the model prompt.
const DefaultMaxTextExtractChars = 100000

// IsTextFile reports whether Slack metadata describes a plain text or Markdown upload.
func IsTextFile(file File) bool {
	mime := strings.ToLower(strings.TrimSpace(file.Mimetype))
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/x-web-markdown", "application/json", "application/yaml", "application/x-yaml":
		return true
	}

	filetype := strings.ToLower(strings.TrimSpace(file.Filetype))
	switch filetype {
	case "markdown", "md", "text", "txt", "json", "yaml", "yml", "log":
		return true
	}

	name := strings.ToLower(strings.TrimSpace(firstNonEmpty(file.Name, file.Title)))
	switch filepath.Ext(name) {
	case ".md", ".markdown", ".txt", ".json", ".yaml", ".yml", ".log":
		return true
	}
	return false
}

// ExtractTextFile reads a UTF-8 text file and returns a normalized, capped excerpt.
func ExtractTextFile(data []byte, maxChars int) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("file is empty")
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("file is not valid UTF-8 text")
	}
	text := normalizePDFText(string(data))
	if strings.ContainsRune(text, '\x00') {
		return "", fmt.Errorf("file appears to be binary")
	}
	if text == "" {
		return "", fmt.Errorf("file contains no readable text")
	}
	if maxChars <= 0 {
		maxChars = DefaultMaxTextExtractChars
	}
	return truncateRunes(text, maxChars), nil
}

// FormatTextExcerpt formats extracted text file content for inclusion in the user message.
func FormatTextExcerpt(displayName, text string) string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = "uploaded.txt"
	}
	return "--- Text file: " + name + " ---\n" + text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

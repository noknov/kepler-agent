package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxToolResultChars = 15000
	previewChars       = 2000
	spillDir           = ".data/tool-spill"
)

var unsafeSpillNameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func spillToolResult(runID, toolName, toolCallID, content string) (string, error) {
	dir := filepath.Join(spillDir, runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	suffix := sanitizeSpillName(toolCallID)
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	if suffix == "" {
		suffix = "result"
	}
	filename := fmt.Sprintf("%s-%s.txt", sanitizeSpillName(toolName), suffix)
	path := filepath.Join(dir, filename)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}

	preview := content
	if len(preview) > previewChars {
		preview = preview[:previewChars]
	}
	return fmt.Sprintf("<persisted-output>\nOutput too large (%d chars). Full output saved to: %s\n\nPreview (first %d chars):\n%s\n...\n</persisted-output>",
		len(content), path, len(preview), preview), nil
}

func maybeSpillResult(runID, toolName, toolCallID, content string) string {
	if len(content) <= maxToolResultChars {
		return content
	}
	spilled, err := spillToolResult(runID, toolName, toolCallID, content)
	if err != nil {
		return content[:maxToolResultChars] + "\n\n[truncated]"
	}
	return spilled
}

func spillRunID(runID string) string {
	runID = strings.TrimSpace(runID)
	if runID != "" {
		return runID
	}
	return "unknown"
}

func sanitizeSpillName(name string) string {
	name = strings.TrimSpace(name)
	name = unsafeSpillNameChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, ".-")
	if name == "" {
		return "unknown"
	}
	return name
}

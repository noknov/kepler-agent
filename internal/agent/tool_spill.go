package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxToolResultChars = 15000
	spillDir           = ".data/tool-spill"
)

func spillToolResult(runID, toolName, toolCallID, content string) (string, error) {
	dir := filepath.Join(spillDir, runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	suffix := toolCallID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	filename := fmt.Sprintf("%s-%s.txt", toolName, suffix)
	path := filepath.Join(dir, filename)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}

	preview := content
	if len(preview) > 2000 {
		preview = preview[:2000]
	}
	return fmt.Sprintf("%s\n\n[Full result (%d chars) saved to %s — use code-read_file to access if needed]",
		preview, len(content), path), nil
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

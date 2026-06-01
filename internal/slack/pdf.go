package slack

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	pdf "github.com/ledongthuc/pdf"
)

// DefaultMaxPDFExtractChars limits how much PDF text is injected into the model prompt.
const DefaultMaxPDFExtractChars = 16000

// IsPDFFile reports whether Slack metadata describes a PDF upload.
func IsPDFFile(file File) bool {
	mime := strings.ToLower(strings.TrimSpace(file.Mimetype))
	if mime == "application/pdf" {
		return true
	}
	return strings.ToLower(strings.TrimSpace(file.Filetype)) == "pdf"
}

// IsPDFData reports whether raw bytes look like a PDF document.
func IsPDFData(data []byte) bool {
	return len(data) >= 5 && string(data[:5]) == "%PDF-"
}

// ExtractPDFText reads plain text from PDF bytes. Scanned/image-only PDFs may return an error.
func ExtractPDFText(data []byte, maxChars int) (string, error) {
	if !IsPDFData(data) {
		return "", fmt.Errorf("not a PDF file")
	}
	if maxChars <= 0 {
		maxChars = DefaultMaxPDFExtractChars
	}
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open pdf: %w", err)
	}
	plain, err := reader.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("extract pdf text: %w", err)
	}
	extracted, err := io.ReadAll(plain)
	if err != nil {
		return "", fmt.Errorf("read pdf text: %w", err)
	}
	text := normalizePDFText(string(extracted))
	if text == "" {
		return "", fmt.Errorf("pdf contains no extractable text")
	}
	return truncateRunes(text, maxChars), nil
}

// FormatPDFExcerpt formats extracted PDF text for inclusion in the user message.
func FormatPDFExcerpt(displayName, text string) string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = "uploaded.pdf"
	}
	return "--- PDF: " + name + " ---\n" + text
}

// FileDisplayName returns a human-readable name for a Slack file attachment.
func FileDisplayName(file File) string {
	if name := strings.TrimSpace(file.Title); name != "" {
		return name
	}
	if name := strings.TrimSpace(file.Name); name != "" {
		return name
	}
	return strings.TrimSpace(file.ID)
}

func normalizePDFText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	trimmed := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" && len(trimmed) > 0 && trimmed[len(trimmed)-1] == "" {
			continue
		}
		trimmed = append(trimmed, line)
	}
	return strings.TrimSpace(strings.Join(trimmed, "\n"))
}

func truncateRunes(text string, maxChars int) string {
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return strings.TrimSpace(string(runes[:maxChars])) + "\n...[truncated]"
}

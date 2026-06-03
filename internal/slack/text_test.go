package slack

import (
	"strings"
	"testing"
)

func TestIsTextFile(t *testing.T) {
	tests := []File{
		{Mimetype: "text/markdown"},
		{Mimetype: "application/x-web-markdown"},
		{Filetype: "markdown"},
		{Name: "runbook.md"},
		{Title: "notes.txt"},
	}
	for _, file := range tests {
		if !IsTextFile(file) {
			t.Fatalf("IsTextFile(%#v) = false, want true", file)
		}
	}
	if IsTextFile(File{Mimetype: "application/pdf", Filetype: "pdf", Name: "invoice.pdf"}) {
		t.Fatal("PDF should not be treated as a text file")
	}
}

func TestExtractTextFile(t *testing.T) {
	got, err := ExtractTextFile([]byte("# Incident\n\nRoot cause"), 12)
	if err != nil {
		t.Fatalf("ExtractTextFile() error = %v", err)
	}
	if !strings.Contains(got, "# Incident") || !strings.Contains(got, "...[truncated]") {
		t.Fatalf("ExtractTextFile() = %q, want capped markdown excerpt", got)
	}
}

func TestExtractTextFileRejectsBinary(t *testing.T) {
	if _, err := ExtractTextFile([]byte{0xff, 0x00, 0x01}, 1000); err == nil {
		t.Fatal("ExtractTextFile() should reject non-text bytes")
	}
}

func TestFormatTextExcerpt(t *testing.T) {
	got := FormatTextExcerpt("runbook.md", "step one")
	if !strings.Contains(got, "--- Text file: runbook.md ---") || !strings.Contains(got, "step one") {
		t.Fatalf("FormatTextExcerpt() = %q", got)
	}
}

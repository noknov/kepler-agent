package slack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsPDFFile(t *testing.T) {
	if !IsPDFFile(File{Mimetype: "application/pdf"}) {
		t.Fatal("application/pdf should be detected")
	}
	if !IsPDFFile(File{Filetype: "pdf"}) {
		t.Fatal("filetype pdf should be detected")
	}
	if IsPDFFile(File{Mimetype: "image/png"}) {
		t.Fatal("png should not be detected as pdf")
	}
}

func TestIsPDFData(t *testing.T) {
	if !IsPDFData([]byte("%PDF-1.4 test")) {
		t.Fatal("pdf magic should match")
	}
	if IsPDFData([]byte("not pdf")) {
		t.Fatal("non-pdf should not match")
	}
}

func TestExtractPDFTextInvalid(t *testing.T) {
	if _, err := ExtractPDFText([]byte("not a pdf"), 1000); err == nil {
		t.Fatal("expected error for non-pdf bytes")
	}
}

func TestExtractPDFTextFromFile(t *testing.T) {
	path := filepath.Join("testdata", "minimal.pdf")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skip("testdata/minimal.pdf not present:", err)
	}
	text, err := ExtractPDFText(data, 4000)
	if err != nil {
		t.Fatalf("ExtractPDFText() error = %v", err)
	}
	if !strings.Contains(text, "This is a heading") {
		t.Fatalf("ExtractPDFText() = %q, want extracted heading text", text)
	}
}

func TestFormatPDFExcerpt(t *testing.T) {
	got := FormatPDFExcerpt("Invoice.pdf", "line one")
	if !strings.Contains(got, "--- PDF: Invoice.pdf ---") || !strings.Contains(got, "line one") {
		t.Fatalf("FormatPDFExcerpt() = %q", got)
	}
}

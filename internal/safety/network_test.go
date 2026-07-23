package safety

import "testing"

func TestValidatePublicHTTPURL(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1/metrics",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/",
		"http://localhost:8080/",
		"file:///etc/passwd",
	} {
		if err := ValidatePublicHTTPURL(raw); err == nil {
			t.Fatalf("ValidatePublicHTTPURL(%q) succeeded, want blocked", raw)
		}
	}
	if err := ValidatePublicHTTPURL("https://www.example.com/path"); err != nil {
		t.Fatalf("public URL rejected: %v", err)
	}
}

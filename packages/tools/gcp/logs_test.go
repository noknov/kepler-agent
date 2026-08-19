package gcp

import (
	"testing"
	"time"
)

func TestParseFreshness(t *testing.T) {
	d, err := parseFreshness("30m")
	if err != nil || d != 30*time.Minute {
		t.Fatalf("parseFreshness(30m) = (%v, %v)", d, err)
	}
	if _, err := parseFreshness("bad"); err == nil {
		t.Fatal("expected error for invalid freshness")
	}
}

func TestFreshnessFilter(t *testing.T) {
	clause, err := freshnessFilter("1h")
	if err != nil {
		t.Fatalf("freshnessFilter: %v", err)
	}
	if !stringsContains(clause, "timestamp>=") {
		t.Fatalf("clause = %q", clause)
	}
}

func TestBuildFilterDefaultsSeverity(t *testing.T) {
	filter := buildFilter("", "", "", "")
	if !stringsContains(filter, "severity>=ERROR") {
		t.Fatalf("filter = %q", filter)
	}
}

func stringsContains(s, part string) bool {
	return len(s) >= len(part) && (s == part || len(part) == 0 || indexOf(s, part) >= 0)
}

func indexOf(s, part string) int {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return i
		}
	}
	return -1
}

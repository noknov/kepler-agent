package memory

import (
	"strings"
	"testing"
)

func TestMemorySelectionIsRelevantAndBounded(t *testing.T) {
	records := []Record{{Key: "language", Kind: "preference", Content: "Prefer Go"}, {Key: "unrelated", Kind: "fact", Content: "unrelated project"}, {Key: "deployment", Kind: "project", Content: "deployment uses region eu"}, {Key: "oversized", Kind: "preference", Content: strings.Repeat("x", 7000)}}
	selected := Select(records, "deployment")
	if len(selected) != 2 || selected[0].Key == "unrelated" || selected[1].Key == "unrelated" {
		t.Fatalf("selection=%v", selected)
	}
	if (Identity{UserID: "u"}).Validate() == nil || (Identity{TeamID: "t"}).Validate() == nil {
		t.Fatal("partial identity accepted")
	}
}

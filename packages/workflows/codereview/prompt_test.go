package codereview

import (
	"strings"
	"testing"
)

func TestParseExplicitCodeReviewCommand(t *testing.T) {
	command, ok := Parse("/cr https://github.com/acme/widgets/pull/42 deep")
	if !ok || command.URL != "https://github.com/acme/widgets/pull/42" || command.Mode != "deep" {
		t.Fatalf("command=%+v ok=%t", command, ok)
	}
}

func TestParseDoesNotHijackOrdinaryReviewQuestion(t *testing.T) {
	if _, ok := Parse("how should we review https://github.com/acme/widgets/pull/42?"); ok {
		t.Fatal("ordinary prompt was routed to code review")
	}
}

func TestParseAllowsMissingURLForHelpfulError(t *testing.T) {
	command, ok := Parse("/cr fast")
	if !ok || command.URL != "" || command.Mode != "fast" {
		t.Fatalf("command=%+v ok=%t", command, ok)
	}
}

func TestFragmentDefinesBoundedVerifiedTeam(t *testing.T) {
	fragment := Fragment(Command{URL: "https://github.com/acme/widgets/pull/42", Mode: "standard"})
	for _, want := range []string{"parallel review", "verification", "Never exceed 5", "same PR URL", "confirmed actionable findings"} {
		if !strings.Contains(fragment.Content, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

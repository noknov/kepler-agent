package codereview

import (
	"strings"
	"testing"
)

func TestParseExplicitCodeReviewCommand(t *testing.T) {
	command, ok := Parse("please review PR https://github.com/acme/widgets/pull/42 deep")
	if !ok || len(command.URLs) != 1 || command.URLs[0] != "https://github.com/acme/widgets/pull/42" || command.Mode != "deep" {
		t.Fatalf("command=%+v ok=%t", command, ok)
	}
}

func TestParseDoesNotHijackOrdinaryReviewQuestion(t *testing.T) {
	if _, ok := Parse("what is a good review process?"); ok {
		t.Fatal("ordinary prompt was routed to code review")
	}
}

func TestParseSupportsChineseIntentAndMultiplePRs(t *testing.T) {
	command, ok := Parse("帮我深度 review 这两个 PR\n<https://github.com/acme/api/pull/42>\n<https://github.com/acme/web/pull/7>")
	if !ok || len(command.URLs) != 2 || command.Mode != "deep" {
		t.Fatalf("command=%+v ok=%t", command, ok)
	}
}

func TestFragmentDefinesBoundedVerifiedTeam(t *testing.T) {
	fragment := Fragment(Command{URLs: []string{"https://github.com/acme/widgets/pull/42"}, Mode: "standard"})
	for _, want := range []string{"parallel review", "verification", "Never exceed 5", "assigned PR", "confirmed actionable findings"} {
		if !strings.Contains(fragment.Content, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

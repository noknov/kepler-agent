package codereview

import (
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/delegation"
	"github.com/noknov/kepler-agent/packages/workflows"
)

func TestScopeValuesRoundTripContinuation(t *testing.T) {
	want := Command{URLs: []string{"https://github.com/acme/widgets/pull/42", "https://github.com/acme/api/pull/7"}}
	got, ok := FromScope(ScopeValues(want))
	if !ok || !got.Continuation || strings.Join(got.URLs, ",") != strings.Join(want.URLs, ",") {
		t.Fatalf("restored=%+v ok=%v", got, ok)
	}
	shared := ScopeValues(want)[delegation.ScopeSharedContext]
	for _, url := range want.URLs {
		if !strings.Contains(shared, url) {
			t.Fatalf("delegation context missing %q: %s", url, shared)
		}
	}
	fragment := Fragment(got)
	if !strings.Contains(fragment.Content, "continuing an existing") || strings.Contains(fragment.Content, "Workflow contract:") {
		t.Fatalf("continuation prompt=%q", fragment.Content)
	}
}

func TestDefinitionStartsFromRoutedPrompt(t *testing.T) {
	definition := Definition{}
	activation, err := definition.StartPrompt("Review https://github.com/acme/widgets/pull/42", nil)
	command, restored := FromScope(activation.Scope)
	if err != nil || !restored || len(command.URLs) != 1 || command.URLs[0] != "https://github.com/acme/widgets/pull/42" {
		t.Fatalf("activation=%+v command=%+v restored=%t err=%v", activation, command, restored, err)
	}
	if activation.OutputPolicy != workflows.OutputFinalOnly {
		t.Fatalf("output policy=%q", activation.OutputPolicy)
	}
}

func TestDefinitionRejectsMissingPullRequest(t *testing.T) {
	if _, err := (Definition{}).StartPrompt("review this", nil); err == nil {
		t.Fatal("missing pull request was accepted")
	}
}

func TestDefinitionPreservesMultiplePullRequestURLs(t *testing.T) {
	activation, err := (Definition{}).StartPrompt("<https://github.com/acme/api/pull/42>\n<https://github.com/acme/web/pull/7>", nil)
	command, restored := FromScope(activation.Scope)
	if err != nil || !restored || len(command.URLs) != 2 {
		t.Fatalf("command=%+v restored=%t err=%v", command, restored, err)
	}
}

func TestFragmentDefinesBoundedVerifiedTeam(t *testing.T) {
	fragment := Fragment(Command{URLs: []string{"https://github.com/acme/widgets/pull/42"}})
	for _, want := range []string{"parallel investigation", "verification", "bounded concurrent workers", "assigned PR", "confirmed actionable findings"} {
		if !strings.Contains(fragment.Content, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

func TestDefinitionAcceptsMoreThanFourPullRequests(t *testing.T) {
	urls := []string{
		"https://github.com/acme/one/pull/1",
		"https://github.com/acme/two/pull/2",
		"https://github.com/acme/three/pull/3",
		"https://github.com/acme/four/pull/4",
		"https://github.com/acme/five/pull/5",
	}
	activation, err := (Definition{}).StartPrompt(strings.Join(urls, "\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	command, ok := FromScope(activation.Scope)
	if !ok || len(command.URLs) != len(urls) {
		t.Fatalf("command=%+v restored=%v", command, ok)
	}
}

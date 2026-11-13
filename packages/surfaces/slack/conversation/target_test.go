package slackconversation

import "testing"

func TestTargetsCannotAliasThreadAndNativeSessions(t *testing.T) {
	native := Target{Kind: CodeChannelSession, Channel: "C1"}
	thread := Target{Channel: "C1", ThreadTS: "1.0"}
	if native.Validate() != nil || thread.Validate() != nil || native.SessionID() == thread.SessionID() {
		t.Fatal("valid targets are not distinct")
	}
	for _, target := range []Target{{Channel: "C1"}, {Kind: CodeChannelSession, Channel: "C1", ThreadTS: "1.0"}, {Kind: "guess", Channel: "C1", ThreadTS: "1.0"}, {Kind: CodeChannelSession}} {
		if target.Validate() == nil {
			t.Fatalf("ambiguous target accepted: %+v", target)
		}
	}
}
